// Package manager wires the concrete application together: config store,
// latency prober, script deployer and settings appliers, behind the
// app.Manager facade the TUI and web UI consume.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/d2d"
	"github.com/Jensen95/shelly-local-comms/internal/mqttdisc"
	"github.com/Jensen95/shelly-local-comms/internal/scripts"
	"github.com/Jensen95/shelly-local-comms/internal/settings"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
	"github.com/Jensen95/shelly-local-comms/internal/transport"
)

// DefaultDiscoveryInterval is how often Start re-scans the LAN for new
// devices when auto-discovery is not configured explicitly.
const DefaultDiscoveryInterval = 5 * time.Minute

// autoDiscoverScanSeconds is the mDNS browse duration of one background
// sweep.
const autoDiscoverScanSeconds = 5

// maxDiscoverSeconds caps a single on-demand discovery scan.
const maxDiscoverSeconds = 60

// Manager is the concrete app.Manager implementation.
type Manager struct {
	store         *app.Store
	prober        *transport.Prober
	deployer      *d2d.Deployer
	discoverEvery time.Duration
	// discoverFn is shelly.Discover in production; tests inject a fake.
	discoverFn func(ctx context.Context, timeout time.Duration) ([]shelly.Device, error)
	// surveyWait is how long to let a deployed BLE survey scan before
	// collecting results; tests shrink it.
	surveyWait time.Duration
	// MQTT announce-discovery. mqttMu guards the service pointer and
	// cancel; runCtx (set by Start) parents the service's lifetime so
	// ApplyMQTT can (re)start discovery in the running process.
	mqttMu     sync.Mutex
	mqtt       *mqttdisc.Service
	mqttCancel context.CancelFunc
	runCtx     context.Context
}

var _ app.Manager = (*Manager)(nil)

// Option configures a Manager.
type Option func(*Manager)

// WithDiscoveryInterval sets how often Start runs a background mDNS sweep
// for new devices. Zero or negative disables auto-discovery.
func WithDiscoveryInterval(d time.Duration) Option {
	return func(m *Manager) { m.discoverEvery = d }
}

// New builds a Manager around the given store.
func New(store *app.Store, opts ...Option) *Manager {
	m := &Manager{
		store:         store,
		discoverEvery: DefaultDiscoveryInterval,
		discoverFn:    shelly.Discover,
		surveyWait:    time.Duration(scripts.SurveyDurationMs)*time.Millisecond + time.Second,
	}
	for _, o := range opts {
		o(m)
	}
	m.prober = transport.NewProber(m.callerFor)
	m.deployer = d2d.NewDeployer(m.callerFor)
	m.prober.SetDevices(store.Config().Devices)
	return m
}

// Start launches background work until ctx ends: the latency probe loop,
// periodic mDNS auto-discovery (unless disabled), and — when a broker has
// been applied — MQTT announce-based discovery, which also finds devices
// on subnets mDNS cannot cross.
func (m *Manager) Start(ctx context.Context) {
	go m.prober.Run(ctx)
	if m.discoverEvery > 0 {
		go m.autoDiscover(ctx)
	}
	m.mqttMu.Lock()
	m.runCtx = ctx
	m.mqttMu.Unlock()
	m.startMQTTDiscovery(m.store.Config().MQTT)
}

// startMQTTDiscovery (re)starts announce-discovery against the broker in
// s, stopping any previous service. A no-op before Start has provided the
// lifetime context, when discovery is disabled, or with no broker set.
func (m *Manager) startMQTTDiscovery(s app.MQTTSettings) {
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	if m.mqttCancel != nil {
		m.mqttCancel()
		m.mqttCancel = nil
		m.mqtt = nil
	}
	if m.runCtx == nil || !s.Enable || s.Server == "" {
		return
	}
	ctx, cancel := context.WithCancel(m.runCtx)
	svc := mqttdisc.NewService(s, m.mergeAnnounced)
	m.mqtt, m.mqttCancel = svc, cancel
	go func() { _ = svc.Run(ctx) }()
}

// mqttService returns the running announce-discovery service, if any.
func (m *Manager) mqttService() *mqttdisc.Service {
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	return m.mqtt
}

// mergeAnnounced registers a device reported by MQTT announce-discovery.
// A known device only gets its address refreshed (announce payloads carry
// no auth flag or name, so the stored full DeviceInfo stays). An unknown
// device is enriched over HTTP first so AuthEnabled and friends are
// accurate; if that fails the announce data is still good enough to list
// it.
func (m *Manager) mergeAnnounced(d shelly.Device) {
	if existing, known := m.deviceByKey(d.Key()); known {
		// Every periodic re-announce lands here; skip the config write
		// and prober reset when nothing changed.
		if existing.Addr == d.Addr {
			return
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if info, err := shelly.NewClient(d.Addr).GetDeviceInfo(ctx); err == nil {
			d.Info = info
		}
		cancel()
	}
	_ = m.store.Update(func(c *app.Config) error {
		for i := range c.Devices {
			if c.Devices[i].Key() == d.Key() {
				c.Devices[i].Addr = d.Addr
				return nil
			}
		}
		c.Devices = append(c.Devices, d)
		return nil
	})
	m.prober.SetDevices(m.store.Config().Devices)
}

// autoDiscover sweeps the LAN immediately and then on every tick, merging
// new devices into the registry. Scan errors are deliberately swallowed:
// a failed sweep is retried on the next tick, and logging here would
// corrupt the TUI's terminal output.
func (m *Manager) autoDiscover(ctx context.Context) {
	scan := func() {
		_, _ = m.Discover(ctx, autoDiscoverScanSeconds)
	}
	scan()
	t := time.NewTicker(m.discoverEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}

// callerFor builds an RPC client for addr, injecting the stored password
// for the device registered at that address, if any.
func (m *Manager) callerFor(addr string) shelly.Caller {
	cfg := m.store.Config()
	var pw string
	for _, d := range cfg.Devices {
		if d.Addr == addr {
			pw = cfg.Passwords[d.Key()]
			break
		}
	}
	return shelly.NewClient(addr, shelly.WithPassword(pw))
}

func (m *Manager) deviceByKey(key string) (shelly.Device, bool) {
	for _, d := range m.store.Config().Devices {
		if d.Key() == key {
			return d, true
		}
	}
	return shelly.Device{}, false
}

// --- Device registry ---

func (m *Manager) Devices() []shelly.Device { return m.store.Config().Devices }

func (m *Manager) Discover(ctx context.Context, timeoutSeconds int) ([]shelly.Device, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	// Cap the browse: an unbounded scan would pin an mDNS listener (and,
	// via the web API, an HTTP handler) for arbitrarily long.
	if timeoutSeconds > maxDiscoverSeconds {
		timeoutSeconds = maxDiscoverSeconds
	}
	// A refresh should sweep every channel: re-solicit MQTT announces too.
	// Their responses arrive asynchronously via mergeAnnounced, so only
	// the mDNS finds are in this call's return value.
	if svc := m.mqttService(); svc != nil {
		svc.RequestAnnounce()
	}
	found, err := m.discoverFn(ctx, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	var fresh []shelly.Device
	err = m.store.Update(func(c *app.Config) error {
		known := map[string]int{}
		for i, d := range c.Devices {
			known[d.Key()] = i
		}
		for _, d := range found {
			if i, ok := known[d.Key()]; ok {
				existing := c.Devices[i]
				d.Source = existing.Source
				if d.BLEMAC == "" {
					d.BLEMAC = existing.BLEMAC
				}
				// Discovery info is sparse when enrichment failed (mDNS TXT
				// carries no MAC, model, name or auth flag — and enrichment
				// always fails for password-protected devices). Keep the
				// last full DeviceInfo in that case: overwriting it would,
				// among other things, drop AuthEnabled and defeat the
				// deployer's auth guard. A successful GetDeviceInfo is
				// recognizable by a non-empty MAC.
				if d.Info.MAC == "" && existing.Info.MAC != "" {
					d.Info = existing.Info
				}
				c.Devices[i] = d
				continue
			}
			c.Devices = append(c.Devices, d)
			fresh = append(fresh, d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.prober.SetDevices(m.store.Config().Devices)
	return fresh, nil
}

func (m *Manager) AddDevice(ctx context.Context, addr, password string) (shelly.Device, error) {
	client := shelly.NewClient(addr, shelly.WithPassword(password))
	info, err := client.GetDeviceInfo(ctx)
	if err != nil {
		return shelly.Device{}, fmt.Errorf("contact device at %s: %w", addr, err)
	}
	dev := shelly.Device{Addr: addr, Info: info, Source: "manual"}
	err = m.store.Update(func(c *app.Config) error {
		for i, d := range c.Devices {
			if d.Key() == dev.Key() {
				dev.BLEMAC = d.BLEMAC
				c.Devices[i] = dev
				m.savePassword(c, dev.Key(), password)
				return nil
			}
		}
		c.Devices = append(c.Devices, dev)
		m.savePassword(c, dev.Key(), password)
		return nil
	})
	if err != nil {
		return shelly.Device{}, err
	}
	m.prober.SetDevices(m.store.Config().Devices)
	return dev, nil
}

func (m *Manager) savePassword(c *app.Config, key, password string) {
	if password == "" {
		return
	}
	if c.Passwords == nil {
		c.Passwords = map[string]string{}
	}
	c.Passwords[key] = password
}

func (m *Manager) RemoveDevice(key string) error {
	err := m.store.Update(func(c *app.Config) error {
		for _, l := range c.Links {
			if l.SourceDevice == key || l.TargetDevice == key {
				return fmt.Errorf("device %s is used by link %q — delete the link first", key, l.Name)
			}
		}
		out := c.Devices[:0]
		found := false
		for _, d := range c.Devices {
			if d.Key() == key {
				found = true
				continue
			}
			out = append(out, d)
		}
		if !found {
			return fmt.Errorf("unknown device %q", key)
		}
		c.Devices = out
		delete(c.Passwords, key)
		return nil
	})
	if err != nil {
		return err
	}
	m.prober.SetDevices(m.store.Config().Devices)
	return nil
}

// --- Links ---

func (m *Manager) Links() []app.Link { return m.store.Config().Links }

func (m *Manager) SaveLink(l app.Link) (app.Link, error) {
	if l.SourceComponent == "" || l.SourceEvent == "" || l.TargetMethod == "" {
		return app.Link{}, errors.New("link needs source component, source event and target method")
	}
	if _, ok := m.deviceByKey(l.SourceDevice); !ok {
		return app.Link{}, fmt.Errorf("unknown source device %q", l.SourceDevice)
	}
	if _, ok := m.deviceByKey(l.TargetDevice); !ok {
		return app.Link{}, fmt.Errorf("unknown target device %q", l.TargetDevice)
	}
	if l.Fallback == (app.FallbackConfig{}) {
		l.Fallback = app.DefaultFallback()
	}
	switch l.Fallback.Strategy {
	case "", app.StrategyFallback:
	case app.StrategyRace:
		if app.NonIdempotentMethod(l.TargetMethod) {
			return app.Link{}, fmt.Errorf("strategy %q needs an idempotent target method: %q may run on both the LAN and BLE path and would undo itself — use an explicit method like Switch.Set, or strategy %q",
				app.StrategyRace, l.TargetMethod, app.StrategyFallback)
		}
	default:
		return app.Link{}, fmt.Errorf("unknown link strategy %q (want %q or %q)", l.Fallback.Strategy, app.StrategyFallback, app.StrategyRace)
	}
	if l.Name == "" {
		l.Name = l.SourceDevice + " → " + l.TargetDevice
	}
	if l.ID == "" {
		l.ID = newID()
	}
	err := m.store.Update(func(c *app.Config) error {
		for i, existing := range c.Links {
			if existing.ID == l.ID {
				l.DeployedScriptID = existing.DeployedScriptID
				l.DeployedAt = existing.DeployedAt
				c.Links[i] = l
				return nil
			}
		}
		c.Links = append(c.Links, l)
		return nil
	})
	return l, err
}

func (m *Manager) DeleteLink(ctx context.Context, id string) error {
	l, ok := m.linkByID(id)
	if !ok {
		return fmt.Errorf("unknown link %q", id)
	}
	if l.DeployedScriptID != 0 {
		if src, ok := m.deviceByKey(l.SourceDevice); ok {
			// Best effort: the link definition must be removable even
			// when the source device is already gone from the network.
			_ = m.deployer.Undeploy(ctx, src, l.DeployedScriptID)
		}
	}
	return m.store.Update(func(c *app.Config) error {
		out := c.Links[:0]
		for _, x := range c.Links {
			if x.ID != id {
				out = append(out, x)
			}
		}
		c.Links = out
		return nil
	})
}

func (m *Manager) linkByID(id string) (app.Link, bool) {
	for _, l := range m.store.Config().Links {
		if l.ID == id {
			return l, true
		}
	}
	return app.Link{}, false
}

func (m *Manager) linkEndpoints(id string) (app.Link, shelly.Device, shelly.Device, error) {
	l, ok := m.linkByID(id)
	if !ok {
		return app.Link{}, shelly.Device{}, shelly.Device{}, fmt.Errorf("unknown link %q", id)
	}
	src, ok := m.deviceByKey(l.SourceDevice)
	if !ok {
		return l, shelly.Device{}, shelly.Device{}, fmt.Errorf("unknown source device %q", l.SourceDevice)
	}
	tgt, ok := m.deviceByKey(l.TargetDevice)
	if !ok {
		return l, src, shelly.Device{}, fmt.Errorf("unknown target device %q", l.TargetDevice)
	}
	return l, src, tgt, nil
}

// tuneFallback seeds the link's LAN timeout floor from the manager's
// observed latency to the target, so a script deployed while the network
// is congested starts with a realistic timeout instead of the static
// default. With no probe samples SuggestedTimeout returns the configured
// floor and this is a no-op.
func (m *Manager) tuneFallback(l app.Link) app.Link {
	suggested := m.prober.SuggestedTimeout(l.TargetDevice, l.Fallback)
	if ms := int(suggested / time.Millisecond); ms > l.Fallback.BaseTimeoutMs {
		l.Fallback.BaseTimeoutMs = ms
	}
	return l
}

func (m *Manager) DeployLink(ctx context.Context, id string) error {
	l, src, tgt, err := m.linkEndpoints(id)
	if err != nil {
		return err
	}
	scriptID, err := m.deployer.Deploy(ctx, src, m.tuneFallback(l), tgt)
	if err != nil {
		return err
	}
	return m.store.Update(func(c *app.Config) error {
		for i, x := range c.Links {
			if x.ID == id {
				c.Links[i].DeployedScriptID = scriptID
				c.Links[i].DeployedAt = time.Now()
			}
		}
		return nil
	})
}

func (m *Manager) RenderLinkScript(id string) (string, error) {
	l, _, tgt, err := m.linkEndpoints(id)
	if err != nil {
		return "", err
	}
	return m.deployer.Render(m.tuneFallback(l), tgt)
}

// --- Shared settings ---

// callersFor resolves device keys (all devices when keys is empty) to RPC
// callers keyed by device key.
func (m *Manager) callersFor(keys []string) (map[string]shelly.Caller, error) {
	cfg := m.store.Config()
	byKey := map[string]shelly.Device{}
	for _, d := range cfg.Devices {
		byKey[d.Key()] = d
	}
	if len(keys) == 0 {
		for k := range byKey {
			keys = append(keys, k)
		}
	}
	out := make(map[string]shelly.Caller, len(keys))
	for _, k := range keys {
		d, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("unknown device %q", k)
		}
		out[k] = m.callerFor(d.Addr)
	}
	return out, nil
}

func (m *Manager) ApplyMQTT(ctx context.Context, s app.MQTTSettings, keys []string) error {
	callers, err := m.callersFor(keys)
	if err != nil {
		return err
	}
	results := settings.ApplyMQTTBulk(ctx, callers, s)
	// Remember the broker and (re)start announce-discovery against it —
	// but only once at least one device actually accepted the config, so
	// a mistyped broker host is not persisted as the discovery target.
	accepted := len(results) == 0
	for _, r := range results {
		if r.Err == nil {
			accepted = true
			break
		}
	}
	if accepted {
		if err := m.store.Update(func(c *app.Config) error {
			c.MQTT = s
			return nil
		}); err != nil {
			return err
		}
		m.startMQTTDiscovery(s)
	}
	return collectResults(ctx, callers, results)
}

func (m *Manager) ApplyBLE(ctx context.Context, s app.BLESettings, keys []string) error {
	callers, err := m.callersFor(keys)
	if err != nil {
		return err
	}
	return collectResults(ctx, callers, settings.ApplyBLEBulk(ctx, callers, s))
}

// collectResults reboots devices whose config change requires it and
// folds per-device failures into one error.
func collectResults(ctx context.Context, callers map[string]shelly.Caller, results []settings.Result) error {
	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Device, r.Err))
			continue
		}
		if r.RestartRequired {
			if err := settings.Reboot(ctx, callers[r.Device]); err != nil {
				errs = append(errs, fmt.Errorf("%s: reboot: %w", r.Device, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) EnableRangeExtender(ctx context.Context, key string, enable bool) error {
	d, ok := m.deviceByKey(key)
	if !ok {
		return fmt.Errorf("unknown device %q", key)
	}
	c := m.callerFor(d.Addr)
	restart, err := settings.SetRangeExtender(ctx, c, enable)
	if err != nil {
		return err
	}
	if restart {
		return settings.Reboot(ctx, c)
	}
	return nil
}

func (m *Manager) JoinExtender(ctx context.Context, edgeKey, extenderKey string) error {
	edge, ok := m.deviceByKey(edgeKey)
	if !ok {
		return fmt.Errorf("unknown edge device %q", edgeKey)
	}
	ext, ok := m.deviceByKey(extenderKey)
	if !ok {
		return fmt.Errorf("unknown extender device %q", extenderKey)
	}
	ap, err := settings.GetAPInfo(ctx, m.callerFor(ext.Addr))
	if err != nil {
		return fmt.Errorf("read AP config of %s: %w", extenderKey, err)
	}
	if !ap.Enabled || !ap.RangeExtender {
		return fmt.Errorf("device %s is not an active range extender — enable it first", extenderKey)
	}
	edgeCaller := m.callerFor(edge.Addr)
	restart, err := settings.JoinExtender(ctx, edgeCaller, ap)
	if err != nil {
		return err
	}
	if restart {
		return settings.Reboot(ctx, edgeCaller)
	}
	return nil
}

// RSSI thresholds (dBm) for extender suggestions: a device at or below
// weakRSSI needs help; a WiFi-fallback candidate must be at least
// minCandidateRSSI and meaningfully stronger than the edge device.
const (
	weakRSSI         = -70
	minCandidateRSSI = -65
	minRSSIGain      = 10
)

// SuggestExtenders finds devices with weak WiFi and suggests the best
// extender for each. Primary method: the weak device itself runs a short
// BLE scan (a temporary script deployed for the duration) and the
// neighbor it hears loudest — that also has a healthy WiFi uplink — is
// suggested: a direct proximity measurement. When the survey cannot run
// (Bluetooth off, old firmware), it falls back to the device with the
// strongest router signal, labeled as such.
func (m *Manager) SuggestExtenders(ctx context.Context) ([]app.ExtenderSuggestion, error) {
	devices := m.store.Config().Devices
	wifi, err := m.wifiReadings(ctx, devices)
	if err != nil {
		return nil, err
	}

	var weak []shelly.Device
	for _, d := range devices {
		if r, ok := wifi[d.Key()]; ok && r <= weakRSSI {
			weak = append(weak, d)
		}
	}
	if len(weak) == 0 {
		return nil, nil
	}

	out := make([]app.ExtenderSuggestion, len(weak))
	var wg sync.WaitGroup
	for i, w := range weak {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = m.suggestFor(ctx, w, wifi, devices)
		}()
	}
	wg.Wait()

	kept := out[:0]
	for _, s := range out {
		if s.Extender != "" {
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].EdgeRSSI < kept[j].EdgeRSSI })
	return kept, nil
}

// wifiReadings returns each reachable device's station RSSI by key.
// Devices without a WiFi uplink (Ethernet/AP-only report no rssi, which
// decodes to 0) are excluded — 0 would otherwise read as the strongest
// possible signal.
func (m *Manager) wifiReadings(ctx context.Context, devices []shelly.Device) (map[string]int, error) {
	type reading struct {
		key  string
		rssi int
	}
	readings := make([]reading, len(devices))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var okCount atomic.Int64
	for i, d := range devices {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := settings.GetWiFiStatus(ctx, m.callerFor(d.Addr))
			if err != nil {
				return
			}
			// The device answered — that counts as a reading even when it
			// has no WiFi uplink (an all-wired fleet is "no suggestions",
			// not an error). rssi>=0 means Ethernet/AP-only: exclude it
			// from the signal map, since 0 would read as strongest.
			okCount.Add(1)
			if st.RSSI >= 0 {
				return
			}
			readings[i] = reading{key: d.Key(), rssi: st.RSSI}
		}()
	}
	wg.Wait()
	if okCount.Load() == 0 && len(devices) > 0 {
		return nil, errors.New("no device reported a WiFi signal reading — cannot measure signal strength")
	}
	wifi := make(map[string]int, len(readings))
	for _, r := range readings {
		if r.key != "" {
			wifi[r.key] = r.rssi
		}
	}
	return wifi, nil
}

// suggestFor produces the suggestion for one weak device; Extender is
// empty when no usable candidate exists.
func (m *Manager) suggestFor(ctx context.Context, w shelly.Device, wifi map[string]int, devices []shelly.Device) app.ExtenderSuggestion {
	edgeRSSI := wifi[w.Key()]

	if seen, err := m.bleSurvey(ctx, w); err == nil {
		for _, n := range matchSurvey(seen, devices, w.Key()) {
			r, ok := wifi[n.key]
			// The extender's own uplink must not be weak, or it cannot
			// bridge anything.
			if !ok || r <= weakRSSI {
				continue
			}
			return app.ExtenderSuggestion{
				Edge: w.Key(), EdgeRSSI: edgeRSSI,
				Extender: n.key, ExtenderRSSI: r,
				BLERSSI: n.bleRSSI, Method: app.SuggestBLEProximity,
			}
		}
	}

	// Fallback: strongest router signal. Distance-blind, so it is held to
	// stricter thresholds and labeled for the UIs.
	bestKey, bestRSSI := "", 0
	for _, d := range devices {
		if d.Key() == w.Key() {
			continue
		}
		if r, ok := wifi[d.Key()]; ok && (bestKey == "" || r > bestRSSI) {
			bestKey, bestRSSI = d.Key(), r
		}
	}
	if bestKey == "" || bestRSSI < minCandidateRSSI || bestRSSI-edgeRSSI < minRSSIGain {
		return app.ExtenderSuggestion{}
	}
	return app.ExtenderSuggestion{
		Edge: w.Key(), EdgeRSSI: edgeRSSI,
		Extender: bestKey, ExtenderRSSI: bestRSSI,
		Method: app.SuggestWiFiFallback,
	}
}

// bleSurvey deploys the temporary scan script to dev, waits out the scan
// window and collects the per-neighbor BLE RSSI map.
func (m *Manager) bleSurvey(ctx context.Context, dev shelly.Device) (map[string]d2d.SurveyEntry, error) {
	id, err := m.deployer.StartSurvey(ctx, dev)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		_, _ = m.deployer.CollectSurvey(context.WithoutCancel(ctx), dev, id)
		return nil, ctx.Err()
	case <-time.After(m.surveyWait):
	}
	return m.deployer.CollectSurvey(ctx, dev, id)
}

// surveyNeighbor is a survey hit matched to a known device.
type surveyNeighbor struct {
	key     string
	bleRSSI int
}

// matchSurvey attributes each scan entry to at most one known device and
// returns the per-device best BLE RSSI, sorted loudest first. Match
// precedence per advertisement: exact stored BLE MAC, then advertised
// local name containing the device id, then the single nearest
// WiFi-MAC-adjacent device (Shelly BLE MACs are derived from the WiFi
// MAC; an ambiguous adjacency tie matches nothing rather than crediting
// the wrong device with a proximity reading).
func matchSurvey(seen map[string]d2d.SurveyEntry, devices []shelly.Device, exclude string) []surveyNeighbor {
	best := map[string]int{}
	for addr, e := range seen {
		key := matchDevice(addr, e, devices, exclude)
		if key == "" {
			continue
		}
		if r, ok := best[key]; !ok || e.RSSI > r {
			best[key] = e.RSSI
		}
	}
	out := make([]surveyNeighbor, 0, len(best))
	for k, r := range best {
		out = append(out, surveyNeighbor{key: k, bleRSSI: r})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].bleRSSI != out[j].bleRSSI {
			return out[i].bleRSSI > out[j].bleRSSI
		}
		return out[i].key < out[j].key
	})
	return out
}

// matchDevice resolves one advertisement to the device it belongs to, or
// "" when unknown/ambiguous.
func matchDevice(addr string, e d2d.SurveyEntry, devices []shelly.Device, exclude string) string {
	naddr := normalizeMAC(addr)
	for _, d := range devices {
		if d.Key() == exclude || d.BLEMAC == "" {
			continue
		}
		if normalizeMAC(d.BLEMAC) == naddr {
			return d.Key()
		}
	}
	if e.Name != "" {
		name := strings.ToLower(e.Name)
		for _, d := range devices {
			if d.Key() == exclude {
				continue
			}
			if id := strings.ToLower(d.Info.ID); id != "" && strings.Contains(name, id) {
				return d.Key()
			}
		}
	}
	bestKey, bestDiff, tie := "", 3, false
	for _, d := range devices {
		if d.Key() == exclude {
			continue
		}
		diff, ok := macDiff(naddr, normalizeMAC(d.Info.MAC))
		if !ok || diff > 2 {
			continue
		}
		switch {
		case diff < bestDiff:
			bestKey, bestDiff, tie = d.Key(), diff, false
		case diff == bestDiff:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return bestKey
}

// macDiff returns the absolute difference of the last byte of two
// normalized MACs sharing the same first five bytes.
func macDiff(na, nb string) (int, bool) {
	if len(na) != 12 || len(nb) != 12 || na[:10] != nb[:10] {
		return 0, false
	}
	la, err1 := strconv.ParseUint(na[10:], 16, 8)
	lb, err2 := strconv.ParseUint(nb[10:], 16, 8)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	diff := int(la) - int(lb)
	if diff < 0 {
		diff = -diff
	}
	return diff, true
}

// normalizeMAC keeps the first 12 hex digits, lowercased (scanner
// addresses may carry separators and a trailing address-type field).
func normalizeMAC(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
			if b.Len() == 12 {
				break
			}
		}
	}
	return b.String()
}

// --- Health ---

func (m *Manager) LatencyStats() []app.LatencyStats { return m.prober.Stats() }

func (m *Manager) ProbeNow(ctx context.Context) []app.LatencyStats {
	return m.prober.ProbeAll(ctx)
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b[:])
}
