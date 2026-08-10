package mqttdisc

import (
	"strings"
	"sync"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// fakeBroker records subscriptions and publishes, and lets tests inject
// incoming messages.
type fakeBroker struct {
	mu     sync.Mutex
	subs   map[string]func(topic string, payload []byte)
	pubs   [][2]string // topic, payload
	pubErr error
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{subs: map[string]func(string, []byte){}}
}

func (f *fakeBroker) Subscribe(topic string, cb func(string, []byte)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[topic] = cb
	return nil
}

func (f *fakeBroker) Publish(topic string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pubs = append(f.pubs, [2]string{topic, string(payload)})
	return f.pubErr
}

// deliver routes a message the way a broker would (exact match or the
// single-level "+" wildcard patterns this package subscribes with).
// Callbacks are invoked outside the lock: handlers publish back into the
// broker (e.g. online → announce request).
func (f *fakeBroker) deliver(t *testing.T, topic string, payload string) {
	t.Helper()
	f.mu.Lock()
	var cbs []func(string, []byte)
	for pattern, cb := range f.subs {
		if topicMatches(pattern, topic) {
			cbs = append(cbs, cb)
		}
	}
	f.mu.Unlock()
	for _, cb := range cbs {
		cb(topic, []byte(payload))
	}
}

func topicMatches(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	pp := strings.Split(pattern, "/")
	tp := strings.Split(topic, "/")
	if len(pp) != len(tp) {
		return false
	}
	for i := range pp {
		if pp[i] != "+" && pp[i] != tp[i] {
			return false
		}
	}
	return true
}

func (f *fakeBroker) published() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.pubs...)
}

func startListener(t *testing.T) (*fakeBroker, *Listener, *[]shelly.Device) {
	t.Helper()
	b := newFakeBroker()
	var got []shelly.Device
	l := NewListener(b, func(d shelly.Device) { got = append(got, d) })
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return b, l, &got
}

func TestStartBroadcastsAnnounce(t *testing.T) {
	b, _, _ := startListener(t)
	pubs := b.published()
	if len(pubs) != 1 || pubs[0] != [2]string{"shellies/command", "announce"} {
		t.Fatalf("publishes = %v, want one announce broadcast", pubs)
	}
}

func TestAnnounceRegistersGen2Device(t *testing.T) {
	b, _, got := startListener(t)
	b.deliver(t, "shellyplus1-aabbcc/announce",
		`{"id":"shellyplus1-aabbcc","mac":"AABBCC","ip":"192.168.1.30","model":"SNSW-001X16EU","gen":2,"ver":"1.4.4"}`)
	if len(*got) != 1 {
		t.Fatalf("devices = %v, want 1", *got)
	}
	d := (*got)[0]
	if d.Addr != "192.168.1.30" || d.Info.ID != "shellyplus1-aabbcc" || d.Info.Gen != 2 || d.Source != "mqtt" {
		t.Fatalf("parsed device = %+v", d)
	}
}

func TestAnnounceIgnoresGen1AndMalformed(t *testing.T) {
	b, _, got := startListener(t)
	b.deliver(t, "shellies/announce",
		`{"id":"shelly1-abc","mac":"ABC","ip":"192.168.1.31","model":"SHSW-1","fw_ver":"v1.14"}`) // no gen => Gen1
	b.deliver(t, "shellyplus1-x/announce", `{"id":"shellyplus1-x","gen":2}`) // no ip
	b.deliver(t, "shellyplus1-y/announce", `not json`)
	if len(*got) != 0 {
		t.Fatalf("devices = %v, want none", *got)
	}
}

func TestOnlineTriggersPerDeviceAnnounceOnce(t *testing.T) {
	b, _, _ := startListener(t)
	b.deliver(t, "shellyplus1-aabbcc/online", "true")
	b.deliver(t, "shellyplus1-aabbcc/online", "true") // duplicate: no second ask
	b.deliver(t, "shellyplus1-ddeeff/online", "false")

	var asks [][2]string
	for _, p := range b.published() {
		if p[0] != "shellies/command" {
			asks = append(asks, p)
		}
	}
	want := [2]string{"shellyplus1-aabbcc/command", "announce"}
	if len(asks) != 1 || asks[0] != want {
		t.Fatalf("per-device asks = %v, want exactly one %v", asks, want)
	}
}

func TestRequestAnnounceRearmsOnlineRequests(t *testing.T) {
	b, l, _ := startListener(t)
	b.deliver(t, "shellyplus1-aabbcc/online", "true")
	// A manual refresh broadcasts again and resets the per-device dedup,
	// so the same device's next online report is asked again.
	if err := l.RequestAnnounce(); err != nil {
		t.Fatalf("RequestAnnounce: %v", err)
	}
	b.deliver(t, "shellyplus1-aabbcc/online", "true")

	broadcasts, asks := 0, 0
	for _, p := range b.published() {
		if p[1] != "announce" {
			t.Fatalf("unexpected payload %v", p)
		}
		switch p[0] {
		case "shellies/command":
			broadcasts++
		case "shellyplus1-aabbcc/command":
			asks++
		}
	}
	if broadcasts != 2 || asks != 2 {
		t.Fatalf("broadcasts = %d, per-device asks = %d; want 2 and 2", broadcasts, asks)
	}
}
