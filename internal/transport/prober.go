// Package transport implements the latency prober: periodic health
// probes of known devices, EWMA round-trip statistics and degradation
// detection. The manager uses it to show per-device LAN health in the
// UIs and to tune the adaptive timeouts baked into generated device
// scripts.
package transport

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// ClientFactory builds the RPC caller for a device address. The
// integrator injects it so the prober stays agnostic of authentication
// (the factory knows the per-device passwords).
type ClientFactory func(addr string) shelly.Caller

// Defaults for the functional options.
const (
	DefaultInterval          = 30 * time.Second
	DefaultEWMAAlpha         = 0.3
	DefaultDegradedThreshold = 300 * time.Millisecond
	DefaultFailureLimit      = 3

	// minProbeTimeout floors the per-probe timeout derived from the
	// degraded threshold.
	minProbeTimeout = time.Second
)

// Option configures a Prober.
type Option func(*Prober)

// WithInterval sets the period of the Run loop. Default 30s.
func WithInterval(d time.Duration) Option {
	return func(p *Prober) {
		if d > 0 {
			p.interval = d
		}
	}
}

// WithEWMAAlpha sets the smoothing factor of the latency EWMA
// (0 < a <= 1; higher weighs recent samples more). Default 0.3.
func WithEWMAAlpha(a float64) Option {
	return func(p *Prober) {
		if a > 0 && a <= 1 {
			p.alpha = a
		}
	}
}

// WithDegradedThreshold sets the EWMA latency above which a device is
// reported degraded. Default 300ms.
func WithDegradedThreshold(d time.Duration) Option {
	return func(p *Prober) {
		if d > 0 {
			p.degradedThreshold = d
		}
	}
}

// WithFailureLimit sets how many consecutive probe failures mark a
// device degraded. Default 3.
func WithFailureLimit(n int) Option {
	return func(p *Prober) {
		if n > 0 {
			p.failureLimit = n
		}
	}
}

// Prober periodically measures RPC round-trip latency to a set of
// devices and keeps per-device EWMA statistics. All methods are safe
// for concurrent use.
type Prober struct {
	factory           ClientFactory
	interval          time.Duration
	alpha             float64
	degradedThreshold time.Duration
	failureLimit      int

	// now is the clock; replaced in tests for deterministic EWMA math.
	now func() time.Time

	mu    sync.Mutex
	addrs map[string]string            // device key -> address
	stats map[string]*app.LatencyStats // device key -> stats
}

// NewProber creates a prober that builds callers with f.
func NewProber(f ClientFactory, opts ...Option) *Prober {
	p := &Prober{
		factory:           f,
		interval:          DefaultInterval,
		alpha:             DefaultEWMAAlpha,
		degradedThreshold: DefaultDegradedThreshold,
		failureLimit:      DefaultFailureLimit,
		now:               time.Now,
		addrs:             map[string]string{},
		stats:             map[string]*app.LatencyStats{},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// SetDevices replaces the probe set. Statistics of devices that remain
// in the set are kept; statistics of removed devices are dropped.
func (p *Prober) SetDevices(devs []shelly.Device) {
	p.mu.Lock()
	defer p.mu.Unlock()

	addrs := make(map[string]string, len(devs))
	for _, d := range devs {
		key := d.Key()
		if _, dup := addrs[key]; dup {
			continue
		}
		addrs[key] = d.Addr
	}
	p.addrs = addrs

	for key := range p.stats {
		if _, ok := addrs[key]; !ok {
			delete(p.stats, key)
		}
	}
	for key := range addrs {
		if _, ok := p.stats[key]; !ok {
			p.stats[key] = &app.LatencyStats{Device: key}
		}
	}
}

// ProbeAll probes every device in the set concurrently, updates the
// statistics and returns the resulting snapshot sorted by device key.
func (p *Prober) ProbeAll(ctx context.Context) []app.LatencyStats {
	p.mu.Lock()
	targets := make(map[string]string, len(p.addrs))
	for key, addr := range p.addrs {
		targets[key] = addr
	}
	timeout := 2 * p.degradedThreshold
	p.mu.Unlock()
	if timeout < minProbeTimeout {
		timeout = minProbeTimeout
	}

	var wg sync.WaitGroup
	for key, addr := range targets {
		wg.Add(1)
		go func(key, addr string) {
			defer wg.Done()
			p.probeOne(ctx, key, addr, timeout)
		}(key, addr)
	}
	wg.Wait()

	return p.Stats()
}

// probeOne makes a single timed Shelly.GetDeviceInfo call and folds the
// outcome into the device's statistics.
func (p *Prober) probeOne(ctx context.Context, key, addr string, timeout time.Duration) {
	caller := p.factory(addr)

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := p.now()
	err := caller.Call(probeCtx, "Shelly.GetDeviceInfo", nil, nil)
	rtt := p.now().Sub(start)

	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.stats[key]
	if !ok {
		// Device was removed while the probe was in flight.
		return
	}
	st.LastProbeAt = p.now()
	if err != nil {
		st.Failures++
		st.LastError = err.Error()
	} else {
		st.Last = rtt
		if st.Samples == 0 {
			st.EWMA = rtt
		} else {
			st.EWMA = time.Duration(p.alpha*float64(rtt) + (1-p.alpha)*float64(st.EWMA))
		}
		st.Samples++
		st.Failures = 0
		st.LastError = ""
	}
	st.Degraded = st.EWMA > p.degradedThreshold || st.Failures >= p.failureLimit
}

// Stats returns a snapshot of the per-device statistics sorted by
// device key. Devices that have not been probed yet appear with
// zero-valued stats.
func (p *Prober) Stats() []app.LatencyStats {
	p.mu.Lock()
	out := make([]app.LatencyStats, 0, len(p.stats))
	for _, st := range p.stats {
		out = append(out, *st)
	}
	p.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// SuggestedTimeout derives the LAN RPC timeout for a device from its
// EWMA latency: clamp(EWMA * fb.LatencyFactor, fb.BaseTimeoutMs,
// fb.MaxTimeoutMs). Without samples (or for an unknown device) it
// returns the base timeout.
func (p *Prober) SuggestedTimeout(key string, fb app.FallbackConfig) time.Duration {
	base := time.Duration(fb.BaseTimeoutMs) * time.Millisecond
	max := time.Duration(fb.MaxTimeoutMs) * time.Millisecond

	p.mu.Lock()
	st, ok := p.stats[key]
	var ewma time.Duration
	var samples int
	if ok {
		ewma, samples = st.EWMA, st.Samples
	}
	p.mu.Unlock()

	if !ok || samples == 0 {
		return base
	}
	t := time.Duration(float64(ewma) * fb.LatencyFactor)
	if t < base {
		return base
	}
	if max > 0 && t > max {
		return max
	}
	return t
}

// Run probes all devices on the configured interval until ctx is done.
// One probe round runs immediately on entry.
func (p *Prober) Run(ctx context.Context) {
	p.ProbeAll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ProbeAll(ctx)
		}
	}
}
