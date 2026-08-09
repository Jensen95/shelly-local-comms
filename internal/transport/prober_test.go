package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// fakeClock is a manual clock the fake caller advances by the simulated
// round-trip, making EWMA math exact in tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeCaller implements shelly.Caller with an injectable simulated
// round-trip time and error.
type fakeCaller struct {
	clock *fakeClock

	mu    sync.Mutex
	rtt   time.Duration
	err   error
	calls int
}

func (f *fakeCaller) set(rtt time.Duration, err error) {
	f.mu.Lock()
	f.rtt, f.err = rtt, err
	f.mu.Unlock()
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCaller) Call(ctx context.Context, method string, params, out any) error {
	if method != "Shelly.GetDeviceInfo" {
		return errors.New("unexpected method " + method)
	}
	f.mu.Lock()
	rtt, err := f.rtt, f.err
	f.calls++
	f.mu.Unlock()
	if f.clock != nil {
		f.clock.advance(rtt)
	}
	return err
}

func dev(key, addr string) shelly.Device {
	return shelly.Device{Addr: addr, Info: shelly.DeviceInfo{ID: key}}
}

// newTestProber wires one fake caller behind every address and returns
// the prober with a deterministic clock.
func newTestProber(t *testing.T, fake *fakeCaller, opts ...Option) *Prober {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1000, 0)}
	fake.clock = clock
	p := NewProber(func(addr string) shelly.Caller { return fake }, opts...)
	p.now = clock.Now
	return p
}

func statFor(t *testing.T, stats []app.LatencyStats, key string) app.LatencyStats {
	t.Helper()
	for _, s := range stats {
		if s.Device == key {
			return s
		}
	}
	t.Fatalf("no stats for device %q in %+v", key, stats)
	return app.LatencyStats{}
}

func TestProbeAllFirstSampleSetsEWMA(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake)
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})

	fake.set(120*time.Millisecond, nil)
	st := statFor(t, p.ProbeAll(context.Background()), "a")

	if st.EWMA != 120*time.Millisecond {
		t.Errorf("EWMA = %v, want first sample 120ms", st.EWMA)
	}
	if st.Last != 120*time.Millisecond {
		t.Errorf("Last = %v, want 120ms", st.Last)
	}
	if st.Samples != 1 {
		t.Errorf("Samples = %d, want 1", st.Samples)
	}
	if st.Failures != 0 || st.LastError != "" || st.Degraded {
		t.Errorf("unexpected failure state: %+v", st)
	}
	if st.LastProbeAt.IsZero() {
		t.Error("LastProbeAt not set")
	}
}

func TestEWMABlend(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake, WithEWMAAlpha(0.5))
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})
	ctx := context.Background()

	fake.set(100*time.Millisecond, nil)
	p.ProbeAll(ctx)
	fake.set(200*time.Millisecond, nil)
	st := statFor(t, p.ProbeAll(ctx), "a")

	// 0.5*200ms + 0.5*100ms = 150ms
	if st.EWMA != 150*time.Millisecond {
		t.Errorf("EWMA = %v, want 150ms", st.EWMA)
	}
	if st.Last != 200*time.Millisecond {
		t.Errorf("Last = %v, want 200ms", st.Last)
	}
	if st.Samples != 2 {
		t.Errorf("Samples = %d, want 2", st.Samples)
	}
}

func TestDegradedOnLatency(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake, WithDegradedThreshold(300*time.Millisecond))
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})
	ctx := context.Background()

	fake.set(500*time.Millisecond, nil)
	if st := statFor(t, p.ProbeAll(ctx), "a"); !st.Degraded {
		t.Errorf("Degraded = false with EWMA %v > 300ms threshold", st.EWMA)
	}

	// Fast probes pull the EWMA back under the threshold (alpha 0.3):
	// 500 -> 350 -> 245.
	fake.set(0, nil)
	p.ProbeAll(ctx)
	if st := statFor(t, p.ProbeAll(ctx), "a"); st.Degraded {
		t.Errorf("Degraded = true after recovery, EWMA %v", st.EWMA)
	}
}

func TestDegradedOnFailuresAndRecovery(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake, WithFailureLimit(2))
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})
	ctx := context.Background()

	fake.set(0, errors.New("connection refused"))
	if st := statFor(t, p.ProbeAll(ctx), "a"); st.Degraded {
		t.Errorf("Degraded = true after 1 failure, limit 2: %+v", st)
	}
	st := statFor(t, p.ProbeAll(ctx), "a")
	if !st.Degraded {
		t.Errorf("Degraded = false after 2 consecutive failures: %+v", st)
	}
	if st.Failures != 2 {
		t.Errorf("Failures = %d, want 2", st.Failures)
	}
	if st.LastError != "connection refused" {
		t.Errorf("LastError = %q, want connection refused", st.LastError)
	}
	if st.Samples != 0 {
		t.Errorf("Samples = %d, failures must not count as samples", st.Samples)
	}

	// One success resets the failure counter and clears degradation.
	fake.set(50*time.Millisecond, nil)
	st = statFor(t, p.ProbeAll(ctx), "a")
	if st.Failures != 0 {
		t.Errorf("Failures = %d after success, want 0", st.Failures)
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q after success, want empty", st.LastError)
	}
	if st.Degraded {
		t.Errorf("Degraded = true after recovery: %+v", st)
	}
	if st.Samples != 1 || st.EWMA != 50*time.Millisecond {
		t.Errorf("Samples/EWMA = %d/%v, want 1/50ms", st.Samples, st.EWMA)
	}
}

func TestSuggestedTimeout(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake)
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})

	fb := app.FallbackConfig{BaseTimeoutMs: 400, MaxTimeoutMs: 1500, LatencyFactor: 4}

	// No samples yet, and unknown device: base timeout.
	if got := p.SuggestedTimeout("a", fb); got != 400*time.Millisecond {
		t.Errorf("no samples: SuggestedTimeout = %v, want 400ms", got)
	}
	if got := p.SuggestedTimeout("nope", fb); got != 400*time.Millisecond {
		t.Errorf("unknown device: SuggestedTimeout = %v, want 400ms", got)
	}

	// EWMA 50ms * 4 = 200ms clamps up to the 400ms floor.
	fake.set(50*time.Millisecond, nil)
	p.ProbeAll(context.Background())
	if got := p.SuggestedTimeout("a", fb); got != 400*time.Millisecond {
		t.Errorf("below floor: SuggestedTimeout = %v, want 400ms", got)
	}

	// EWMA 200ms * 4 = 800ms sits between floor and cap.
	p2 := newTestProber(t, &fakeCaller{}, WithEWMAAlpha(0.3))
	p2.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})
	p2.mu.Lock()
	p2.stats["a"].EWMA = 200 * time.Millisecond
	p2.stats["a"].Samples = 1
	p2.mu.Unlock()
	if got := p2.SuggestedTimeout("a", fb); got != 800*time.Millisecond {
		t.Errorf("in range: SuggestedTimeout = %v, want 800ms", got)
	}

	// EWMA 600ms * 4 = 2400ms clamps down to the 1500ms cap.
	p2.mu.Lock()
	p2.stats["a"].EWMA = 600 * time.Millisecond
	p2.mu.Unlock()
	if got := p2.SuggestedTimeout("a", fb); got != 1500*time.Millisecond {
		t.Errorf("above cap: SuggestedTimeout = %v, want 1500ms", got)
	}
}

func TestSetDevicesPreservesStats(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake)
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1"), dev("b", "10.0.0.2")})

	fake.set(100*time.Millisecond, nil)
	p.ProbeAll(context.Background())

	// Keep a, drop b, add c.
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1"), dev("c", "10.0.0.3")})

	stats := p.Stats()
	if len(stats) != 2 {
		t.Fatalf("Stats() has %d entries, want 2: %+v", len(stats), stats)
	}
	a := statFor(t, stats, "a")
	if a.Samples != 1 || a.EWMA != 100*time.Millisecond {
		t.Errorf("stats for kept device a lost: %+v", a)
	}
	c := statFor(t, stats, "c")
	if c.Samples != 0 || c.EWMA != 0 {
		t.Errorf("new device c should have zero stats: %+v", c)
	}
	for _, s := range stats {
		if s.Device == "b" {
			t.Errorf("removed device b still present: %+v", s)
		}
	}
}

func TestStatsSortedByKey(t *testing.T) {
	fake := &fakeCaller{}
	p := newTestProber(t, fake)
	p.SetDevices([]shelly.Device{dev("zeta", "1"), dev("alpha", "2"), dev("mid", "3")})

	fake.set(10*time.Millisecond, nil)
	stats := p.ProbeAll(context.Background())

	want := []string{"alpha", "mid", "zeta"}
	if len(stats) != len(want) {
		t.Fatalf("got %d stats, want %d", len(stats), len(want))
	}
	for i, w := range want {
		if stats[i].Device != w {
			t.Errorf("stats[%d].Device = %q, want %q", i, stats[i].Device, w)
		}
	}
}

func TestRunProbesUntilCancelled(t *testing.T) {
	var calls atomic.Int64
	caller := callerFunc(func(ctx context.Context, method string, params, out any) error {
		calls.Add(1)
		return nil
	})
	p := NewProber(func(addr string) shelly.Caller { return caller },
		WithInterval(5*time.Millisecond))
	p.SetDevices([]shelly.Device{dev("a", "10.0.0.1")})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d probe calls before deadline", calls.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// callerFunc adapts a function to shelly.Caller.
type callerFunc func(ctx context.Context, method string, params, out any) error

func (f callerFunc) Call(ctx context.Context, method string, params, out any) error {
	return f(ctx, method, params, out)
}
