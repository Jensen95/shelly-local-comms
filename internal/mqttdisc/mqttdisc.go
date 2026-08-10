// Package mqttdisc discovers Shelly devices through an MQTT broker.
//
// Shellys configured for MQTT answer an "announce" command by publishing
// their identity — including their IP — and publish their online state
// via the broker (last will). Both cross subnet/VLAN boundaries that stop
// mDNS, so any device that can reach the broker becomes discoverable:
//
//   - subscribe to `+/announce` (per-device topics) and
//     `shellies/announce` (the shared topic Gen2+ devices also publish
//     their announce to)
//   - broadcast "announce" to `shellies/command` on connect
//   - when `<prefix>/online` reports true, ask that device directly via
//     `<prefix>/command`
package mqttdisc

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// Broker is the minimal MQTT surface the listener needs. pahoBroker
// implements it against a real broker; tests use a fake.
type Broker interface {
	Subscribe(topic string, cb func(topic string, payload []byte)) error
	Publish(topic string, payload []byte) error
}

// Listener turns announce traffic on a broker into device callbacks.
type Listener struct {
	b             Broker
	onDevice      func(shelly.Device)
	extraPrefixes []string

	mu        sync.Mutex
	requested map[string]bool
}

// ListenerOption configures a Listener.
type ListenerOption func(*Listener)

// WithExtraPrefix adds a device topic prefix to subscribe explicitly.
// The default "+/announce" and "+/online" wildcards match one topic level
// only, so a prefix containing "/" (which this tool itself can configure
// via MQTT settings) would otherwise be missed. Empty or single-level
// prefixes are ignored — the wildcard already covers them.
func WithExtraPrefix(prefix string) ListenerOption {
	return func(l *Listener) {
		if strings.Contains(prefix, "/") {
			l.extraPrefixes = append(l.extraPrefixes, prefix)
		}
	}
}

// NewListener creates a listener that reports every announced device to
// onDevice. Call Start to subscribe and solicit announcements.
func NewListener(b Broker, onDevice func(shelly.Device), opts ...ListenerOption) *Listener {
	l := &Listener{b: b, onDevice: onDevice, requested: map[string]bool{}}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Start subscribes to the announce and online topics and broadcasts an
// announce request.
func (l *Listener) Start() error {
	// Gen2+ devices publish their announce to BOTH <prefix>/announce and
	// the shared shellies/announce topic, so the shared topic catches
	// devices whose prefix the wildcard misses; Gen1-only payloads
	// arriving there are filtered by parseAnnounce.
	type sub struct {
		topic string
		cb    func(string, []byte)
	}
	subs := []sub{
		{"+/announce", l.handleAnnounce},
		{"shellies/announce", l.handleAnnounce},
		{"+/online", l.handleOnline},
	}
	for _, p := range l.extraPrefixes {
		subs = append(subs,
			sub{p + "/announce", l.handleAnnounce},
			sub{p + "/online", l.handleOnline})
	}
	for _, s := range subs {
		if err := l.b.Subscribe(s.topic, s.cb); err != nil {
			return err
		}
	}
	return l.RequestAnnounce()
}

// RequestAnnounce broadcasts an announce request to every device on the
// broker (Gen2+ devices subscribe to shellies/command as well) and
// re-arms the per-device online-triggered requests.
func (l *Listener) RequestAnnounce() error {
	l.mu.Lock()
	l.requested = map[string]bool{}
	l.mu.Unlock()
	return l.b.Publish("shellies/command", []byte("announce"))
}

func (l *Listener) handleAnnounce(_ string, payload []byte) {
	dev, ok := parseAnnounce(payload)
	if !ok {
		return
	}
	l.onDevice(dev)
}

// handleOnline requests an announce from a device the first time its
// online topic reports true.
func (l *Listener) handleOnline(topic string, payload []byte) {
	if strings.TrimSpace(string(payload)) != "true" {
		return
	}
	prefix := strings.TrimSuffix(topic, "/online")
	if prefix == topic || prefix == "" {
		return
	}
	l.mu.Lock()
	seen := l.requested[prefix]
	l.requested[prefix] = true
	l.mu.Unlock()
	if seen {
		return
	}
	_ = l.b.Publish(prefix+"/command", []byte("announce"))
}

// announcePayload covers both the Gen1 and Gen2+ announce formats.
type announcePayload struct {
	ID    string `json:"id"`
	MAC   string `json:"mac"`
	IP    string `json:"ip"`
	Model string `json:"model"`
	Gen   int    `json:"gen"`
	App   string `json:"app"`
	Ver   string `json:"ver"`
	FWVer string `json:"fw_ver"` // Gen1 field name
	FWID  string `json:"fw_id"`
}

// parseAnnounce converts an announce message into a Device. Messages
// without an id or IP are useless for registration and are dropped, as
// are Gen1 devices — this tool speaks the Gen2+ RPC protocol only.
func parseAnnounce(payload []byte) (shelly.Device, bool) {
	var a announcePayload
	if err := json.Unmarshal(payload, &a); err != nil {
		return shelly.Device{}, false
	}
	if a.ID == "" || a.IP == "" {
		return shelly.Device{}, false
	}
	if a.Gen < 2 {
		return shelly.Device{}, false
	}
	ver := a.Ver
	if ver == "" {
		ver = a.FWVer
	}
	return shelly.Device{
		Addr:   a.IP,
		Source: "mqtt",
		Info: shelly.DeviceInfo{
			ID:         a.ID,
			MAC:        a.MAC,
			Model:      a.Model,
			Gen:        a.Gen,
			App:        a.App,
			Version:    ver,
			FirmwareID: a.FWID,
		},
	}, true
}
