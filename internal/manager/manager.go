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
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/d2d"
	"github.com/Jensen95/shelly-local-comms/internal/settings"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
	"github.com/Jensen95/shelly-local-comms/internal/transport"
)

// Manager is the concrete app.Manager implementation.
type Manager struct {
	store    *app.Store
	prober   *transport.Prober
	deployer *d2d.Deployer
}

var _ app.Manager = (*Manager)(nil)

// New builds a Manager around the given store.
func New(store *app.Store) *Manager {
	m := &Manager{store: store}
	m.prober = transport.NewProber(m.callerFor)
	m.deployer = d2d.NewDeployer(m.callerFor)
	m.prober.SetDevices(store.Config().Devices)
	return m
}

// Start launches background work (the latency probe loop) until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	go m.prober.Run(ctx)
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
	found, err := shelly.Discover(ctx, time.Duration(timeoutSeconds)*time.Second)
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
				d.Source = c.Devices[i].Source
				if d.BLEMAC == "" {
					d.BLEMAC = c.Devices[i].BLEMAC
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

func (m *Manager) DeployLink(ctx context.Context, id string) error {
	l, src, tgt, err := m.linkEndpoints(id)
	if err != nil {
		return err
	}
	scriptID, err := m.deployer.Deploy(ctx, src, l, tgt)
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
	return m.deployer.Render(l, tgt)
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
	return collectResults(ctx, callers, settings.ApplyMQTTBulk(ctx, callers, s))
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
