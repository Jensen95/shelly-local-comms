package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// fakeManager implements app.Manager with canned data and call recording.
type fakeManager struct {
	devices []shelly.Device
	links   []app.Link
	stats   []app.LatencyStats

	discoverFound []shelly.Device
	discoverErr   error
	discoverCalls int

	addedAddrs   []string
	addedPasses  []string
	removedKeys  []string
	savedLinks   []app.Link
	saveErr      error
	deletedLinks []string
	deployedIDs  []string
	deployErr    error
	renderedIDs  []string
	renderScript string
	renderErr    error

	mqttApplied []app.MQTTSettings
	mqttKeys    [][]string
	mqttErr     error
	bleApplied  []app.BLESettings
	bleKeys     [][]string

	extToggles [][2]string // key, enable
	joins      [][2]string // edge, extender

	probeCalls int
}

var _ app.Manager = (*fakeManager)(nil)

func (f *fakeManager) Devices() []shelly.Device { return f.devices }

func (f *fakeManager) Discover(ctx context.Context, timeoutSeconds int) ([]shelly.Device, error) {
	f.discoverCalls++
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	f.devices = append(f.devices, f.discoverFound...)
	return f.discoverFound, nil
}

func (f *fakeManager) AddDevice(ctx context.Context, addr, password string) (shelly.Device, error) {
	f.addedAddrs = append(f.addedAddrs, addr)
	f.addedPasses = append(f.addedPasses, password)
	dev := shelly.Device{Addr: addr, Source: "manual"}
	f.devices = append(f.devices, dev)
	return dev, nil
}

func (f *fakeManager) RemoveDevice(key string) error {
	f.removedKeys = append(f.removedKeys, key)
	kept := f.devices[:0]
	for _, d := range f.devices {
		if d.Key() != key {
			kept = append(kept, d)
		}
	}
	f.devices = kept
	return nil
}

func (f *fakeManager) Links() []app.Link { return f.links }

func (f *fakeManager) SaveLink(l app.Link) (app.Link, error) {
	if f.saveErr != nil {
		return app.Link{}, f.saveErr
	}
	if l.ID == "" {
		l.ID = fmt.Sprintf("link-%d", len(f.savedLinks)+1)
	}
	f.savedLinks = append(f.savedLinks, l)
	f.links = append(f.links, l)
	return l, nil
}

func (f *fakeManager) DeleteLink(ctx context.Context, id string) error {
	f.deletedLinks = append(f.deletedLinks, id)
	kept := f.links[:0]
	for _, l := range f.links {
		if l.ID != id {
			kept = append(kept, l)
		}
	}
	f.links = kept
	return nil
}

func (f *fakeManager) DeployLink(ctx context.Context, id string) error {
	f.deployedIDs = append(f.deployedIDs, id)
	return f.deployErr
}

func (f *fakeManager) RenderLinkScript(id string) (string, error) {
	f.renderedIDs = append(f.renderedIDs, id)
	return f.renderScript, f.renderErr
}

func (f *fakeManager) ApplyMQTT(ctx context.Context, s app.MQTTSettings, keys []string) error {
	f.mqttApplied = append(f.mqttApplied, s)
	f.mqttKeys = append(f.mqttKeys, keys)
	return f.mqttErr
}

func (f *fakeManager) ApplyBLE(ctx context.Context, s app.BLESettings, keys []string) error {
	f.bleApplied = append(f.bleApplied, s)
	f.bleKeys = append(f.bleKeys, keys)
	return nil
}

func (f *fakeManager) EnableRangeExtender(ctx context.Context, key string, enable bool) error {
	f.extToggles = append(f.extToggles, [2]string{key, fmt.Sprint(enable)})
	return nil
}

func (f *fakeManager) JoinExtender(ctx context.Context, edgeKey, extenderKey string) error {
	f.joins = append(f.joins, [2]string{edgeKey, extenderKey})
	return nil
}

func (f *fakeManager) LatencyStats() []app.LatencyStats { return f.stats }

func (f *fakeManager) ProbeNow(ctx context.Context) []app.LatencyStats {
	f.probeCalls++
	return f.stats
}

// --- test helpers ---

func twoDevices() []shelly.Device {
	return []shelly.Device{
		{Addr: "192.168.1.10", Info: shelly.DeviceInfo{ID: "shelly1-aaa", Name: "Kitchen", Model: "SNSW-001X16", Gen: 2}},
		{Addr: "192.168.1.11", Info: shelly.DeviceInfo{ID: "shelly1-bbb", Name: "Hall", Model: "SNSW-001X16", Gen: 3}},
	}
}

func newTestModel(t *testing.T, f *fakeManager) Model {
	t.Helper()
	m := New(f)
	return feed(t, m, tea.WindowSizeMsg{Width: 130, Height: 40})
}

// keyMsg turns a readable key name into a tea.KeyMsg.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// press feeds one key through Update and fully runs any resulting
// commands (executing tea.Cmds synchronously, feeding messages back).
func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = feed(t, m, keyMsg(k))
	}
	return m
}

// typeText feeds a string rune-by-rune as key presses.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			m = feed(t, m, keyMsg("space"))
			continue
		}
		m = feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// feed sends a message through Update and synchronously executes any
// returned commands, feeding their messages back into the model.
// Spinner ticks are dropped to avoid an endless tick loop.
func feed(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	if msg == nil {
		return m
	}
	if _, ok := msg.(spinner.TickMsg); ok {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c)
		}
		return m
	}
	updated, cmd := m.Update(msg)
	return runCmd(t, updated.(Model), cmd)
}

func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feed(t, m, cmd())
}

func mustContain(t *testing.T, view, want string) {
	t.Helper()
	if !strings.Contains(view, want) {
		t.Fatalf("view does not contain %q\n---- view ----\n%s", want, view)
	}
}

// --- tests ---

func TestTabSwitching(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	mustContain(t, m.View(), "Address") // devices table header

	m = press(t, m, "2")
	mustContain(t, m.View(), "Source (dev comp event)")

	m = press(t, m, "tab")
	mustContain(t, m.View(), "Failures") // health table header

	m = press(t, m, "tab")
	mustContain(t, m.View(), "MQTT") // settings menu

	m = press(t, m, "shift+tab")
	mustContain(t, m.View(), "Failures")

	m = press(t, m, "1")
	mustContain(t, m.View(), "Address")
}

func TestDevicesTableRenderingWithDegraded(t *testing.T) {
	f := &fakeManager{
		devices: twoDevices(),
		stats: []app.LatencyStats{
			{Device: "shelly1-aaa", EWMA: 12 * time.Millisecond, Last: 10 * time.Millisecond, Samples: 5},
			{Device: "shelly1-bbb", EWMA: 250 * time.Millisecond, Last: 300 * time.Millisecond, Samples: 8, Failures: 3, Degraded: true, LastError: "timeout"},
		},
	}
	m := newTestModel(t, f)
	view := m.View()

	mustContain(t, view, "Kitchen")
	mustContain(t, view, "Hall")
	mustContain(t, view, "192.168.1.10")
	mustContain(t, view, "12.0")
	mustContain(t, view, "250.0")
	mustContain(t, view, "DEGRADED")
	mustContain(t, view, "ok")
}

func TestDevicesUnknownStatusWithoutStats(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)
	mustContain(t, m.View(), "unknown")
}

func TestDiscoverFlow(t *testing.T) {
	f := &fakeManager{
		devices: twoDevices()[:1],
		discoverFound: []shelly.Device{
			{Addr: "192.168.1.20", Info: shelly.DeviceInfo{ID: "shelly1-new", Name: "Garage", Gen: 2}},
		},
	}
	m := newTestModel(t, f)

	m = press(t, m, "d")

	if f.discoverCalls != 1 {
		t.Fatalf("Discover called %d times, want 1", f.discoverCalls)
	}
	view := m.View()
	mustContain(t, view, "discovery finished: 1 new device(s)")
	mustContain(t, view, "Garage")
}

func TestDiscoverError(t *testing.T) {
	f := &fakeManager{devices: twoDevices(), discoverErr: errors.New("mdns socket busy")}
	m := newTestModel(t, f)
	m = press(t, m, "d")
	mustContain(t, m.View(), "discovery failed: mdns socket busy")
}

func TestProbeNowFromHealthTab(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)
	m = press(t, m, "3", "p")
	if f.probeCalls != 1 {
		t.Fatalf("ProbeNow called %d times, want 1", f.probeCalls)
	}
	mustContain(t, m.View(), "probe complete")
}

func TestAddDeviceForm(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "a")
	mustContain(t, m.View(), "Add device manually")
	m = typeText(t, m, "192.168.1.99")
	m = press(t, m, "tab")
	m = typeText(t, m, "hunter2")
	m = press(t, m, "enter")

	if len(f.addedAddrs) != 1 || f.addedAddrs[0] != "192.168.1.99" {
		t.Fatalf("AddDevice addrs = %v, want [192.168.1.99]", f.addedAddrs)
	}
	if f.addedPasses[0] != "hunter2" {
		t.Fatalf("AddDevice password = %q, want hunter2", f.addedPasses[0])
	}
	mustContain(t, m.View(), "added device 192.168.1.99")
}

func TestLinkWizardHappyPath(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "2", "n")
	mustContain(t, m.View(), "New link")

	// Step: name.
	m = typeText(t, m, "hall to lamp")
	m = press(t, m, "enter")
	// Step: source device — keep first (Kitchen / shelly1-aaa).
	m = press(t, m, "enter")
	// Step: source component — keep default input:0.
	m = press(t, m, "enter")
	// Step: event — cycle once to single_push.
	m = press(t, m, "down", "enter")
	// Step: target device — move to second (Hall / shelly1-bbb).
	m = press(t, m, "down", "enter")
	// Step: target method — keep default Switch.Toggle.
	m = press(t, m, "enter")
	// Step: params JSON — keep default {"id":0}.
	m = press(t, m, "enter")
	// Step: BLE fallback (default on) — enter saves.
	m = press(t, m, "enter")

	if len(f.savedLinks) != 1 {
		t.Fatalf("SaveLink called %d times, want 1", len(f.savedLinks))
	}
	got := f.savedLinks[0]
	if got.Name != "hall to lamp" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.SourceDevice != "shelly1-aaa" {
		t.Errorf("SourceDevice = %q, want shelly1-aaa", got.SourceDevice)
	}
	if got.SourceComponent != "input:0" {
		t.Errorf("SourceComponent = %q, want input:0", got.SourceComponent)
	}
	if got.SourceEvent != "single_push" {
		t.Errorf("SourceEvent = %q, want single_push", got.SourceEvent)
	}
	if got.TargetDevice != "shelly1-bbb" {
		t.Errorf("TargetDevice = %q, want shelly1-bbb", got.TargetDevice)
	}
	if got.TargetMethod != "Switch.Toggle" {
		t.Errorf("TargetMethod = %q, want Switch.Toggle", got.TargetMethod)
	}
	if len(got.TargetParams) != 1 || got.TargetParams["id"] != float64(0) {
		t.Errorf("TargetParams = %v, want map[id:0]", got.TargetParams)
	}
	if !got.Fallback.BLEEnabled {
		t.Errorf("Fallback.BLEEnabled = false, want true")
	}
	if got.Fallback.BaseTimeoutMs != app.DefaultFallback().BaseTimeoutMs {
		t.Errorf("Fallback.BaseTimeoutMs = %d, want default", got.Fallback.BaseTimeoutMs)
	}

	view := m.View()
	mustContain(t, view, `link "hall to lamp" saved`)
	mustContain(t, view, "hall to lamp")
	mustContain(t, view, "not deployed")
}

func TestLinkWizardInvalidParamsJSON(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "2", "n")
	// name, source, component, event, target, method -> params step.
	m = press(t, m, "enter", "enter", "enter", "enter", "enter", "enter")
	// Corrupt the params JSON.
	m = press(t, m, "ctrl+u")
	m = typeText(t, m, "{nope")
	// Params -> BLE step -> attempt save.
	m = press(t, m, "enter", "enter")

	if len(f.savedLinks) != 0 {
		t.Fatalf("SaveLink called %d times, want 0", len(f.savedLinks))
	}
	mustContain(t, m.View(), "invalid params JSON")

	// Fix the JSON and save successfully.
	m = press(t, m, "ctrl+u")
	m = typeText(t, m, `{"id":1}`)
	m = press(t, m, "enter", "enter")
	if len(f.savedLinks) != 1 {
		t.Fatalf("SaveLink called %d times after fix, want 1", len(f.savedLinks))
	}
	if f.savedLinks[0].TargetParams["id"] != float64(1) {
		t.Errorf("TargetParams = %v, want map[id:1]", f.savedLinks[0].TargetParams)
	}
}

func TestDeployLink(t *testing.T) {
	f := &fakeManager{
		devices: twoDevices(),
		links: []app.Link{{
			ID: "l1", Name: "hall-link",
			SourceDevice: "shelly1-aaa", SourceComponent: "input:0", SourceEvent: "toggle",
			TargetDevice: "shelly1-bbb", TargetMethod: "Switch.Toggle",
			Fallback: app.DefaultFallback(),
		}},
	}
	m := newTestModel(t, f)

	m = press(t, m, "2")
	mustContain(t, m.View(), "hall-link")
	m = press(t, m, "enter")

	if len(f.deployedIDs) != 1 || f.deployedIDs[0] != "l1" {
		t.Fatalf("DeployLink ids = %v, want [l1]", f.deployedIDs)
	}
	mustContain(t, m.View(), "link deployed")
}

func TestDeployLinkError(t *testing.T) {
	f := &fakeManager{
		devices:   twoDevices(),
		deployErr: errors.New("script upload rejected"),
		links: []app.Link{{
			ID: "l1", Name: "hall-link",
			SourceDevice: "shelly1-aaa", TargetDevice: "shelly1-bbb",
			TargetMethod: "Switch.Toggle", SourceComponent: "input:0", SourceEvent: "toggle",
		}},
	}
	m := newTestModel(t, f)
	m = press(t, m, "2", "enter")
	mustContain(t, m.View(), "deploy link failed: script upload rejected")
}

func TestScriptPreview(t *testing.T) {
	f := &fakeManager{
		devices:      twoDevices(),
		renderScript: "// mJS link script\nlet EWMA = 0;",
		links: []app.Link{{
			ID: "l1", Name: "hall-link",
			SourceDevice: "shelly1-aaa", TargetDevice: "shelly1-bbb",
			TargetMethod: "Switch.Toggle", SourceComponent: "input:0", SourceEvent: "toggle",
		}},
	}
	m := newTestModel(t, f)
	m = press(t, m, "2", "v")

	if len(f.renderedIDs) != 1 || f.renderedIDs[0] != "l1" {
		t.Fatalf("RenderLinkScript ids = %v, want [l1]", f.renderedIDs)
	}
	view := m.View()
	mustContain(t, view, "Script preview")
	mustContain(t, view, "// mJS link script")

	m = press(t, m, "esc")
	mustContain(t, m.View(), "Source (dev comp event)")
}

func TestDeleteLinkWithConfirm(t *testing.T) {
	f := &fakeManager{
		devices: twoDevices(),
		links: []app.Link{{
			ID: "l1", Name: "hall-link",
			SourceDevice: "shelly1-aaa", TargetDevice: "shelly1-bbb",
			TargetMethod: "Switch.Toggle", SourceComponent: "input:0", SourceEvent: "toggle",
		}},
	}
	m := newTestModel(t, f)

	// Abort first.
	m = press(t, m, "2", "x")
	mustContain(t, m.View(), "Delete link")
	m = press(t, m, "n")
	if len(f.deletedLinks) != 0 {
		t.Fatalf("DeleteLink called after abort")
	}

	// Then confirm.
	m = press(t, m, "x", "y")
	if len(f.deletedLinks) != 1 || f.deletedLinks[0] != "l1" {
		t.Fatalf("DeleteLink ids = %v, want [l1]", f.deletedLinks)
	}
}

func TestHealthTabRendering(t *testing.T) {
	f := &fakeManager{
		devices: twoDevices(),
		stats: []app.LatencyStats{
			{Device: "shelly1-bbb", EWMA: 250 * time.Millisecond, Last: 301 * time.Millisecond, Samples: 8, Failures: 3, Degraded: true, LastError: "timeout"},
		},
	}
	m := newTestModel(t, f)
	m = press(t, m, "3")
	view := m.View()
	mustContain(t, view, "shelly1-bbb")
	mustContain(t, view, "301.0")
	mustContain(t, view, "250.0")
	mustContain(t, view, "yes")
	mustContain(t, view, "timeout")
	mustContain(t, view, "BLE fallback")
}

func TestSettingsMQTTApply(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "4")
	mustContain(t, m.View(), "MQTT")
	m = press(t, m, "enter") // open MQTT form
	mustContain(t, m.View(), "Apply to all devices")

	m = press(t, m, "space") // toggle Enable on
	m = press(t, m, "down")  // -> server
	m = typeText(t, m, "10.0.0.5:1883")
	m = press(t, m, "down") // -> user
	m = typeText(t, m, "shelly")
	m = press(t, m, "down") // -> password
	m = typeText(t, m, "s3cret")
	m = press(t, m, "down") // -> topic prefix
	m = typeText(t, m, "home")
	m = press(t, m, "down")  // -> rpc_ntf
	m = press(t, m, "space") // toggle rpc_ntf (default on -> off)
	m = press(t, m, "down")  // -> status_ntf, leave on
	m = press(t, m, "down")  // -> apply
	m = press(t, m, "enter")

	if len(f.mqttApplied) != 1 {
		t.Fatalf("ApplyMQTT called %d times, want 1", len(f.mqttApplied))
	}
	got := f.mqttApplied[0]
	want := app.MQTTSettings{
		Enable: true, Server: "10.0.0.5:1883", User: "shelly",
		Password: "s3cret", TopicPrefix: "home",
		RPCNotifs: false, StatusNotifs: true,
	}
	if got != want {
		t.Errorf("ApplyMQTT settings = %+v, want %+v", got, want)
	}
	if len(f.mqttKeys[0]) != 0 {
		t.Errorf("ApplyMQTT keys = %v, want empty (all devices)", f.mqttKeys[0])
	}
	mustContain(t, m.View(), "MQTT settings applied to all devices")
}

func TestSettingsBLEApply(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "4", "down", "enter") // open Bluetooth form
	mustContain(t, m.View(), "Observer mode")

	m = press(t, m, "down", "down", "space") // toggle observer on
	m = press(t, m, "down", "enter")         // apply

	if len(f.bleApplied) != 1 {
		t.Fatalf("ApplyBLE called %d times, want 1", len(f.bleApplied))
	}
	got := f.bleApplied[0]
	want := app.BLESettings{Enable: true, RPC: true, Observer: true}
	if got != want {
		t.Errorf("ApplyBLE settings = %+v, want %+v", got, want)
	}
}

func TestSettingsRangeExtenderToggleAndPair(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	// Enable extender on first device.
	m = press(t, m, "4", "down", "down", "enter") // range extender submenu
	mustContain(t, m.View(), "Range extender")
	m = press(t, m, "enter") // toggle flow, device list
	mustContain(t, m.View(), "pick a device")
	m = press(t, m, "enter") // enable on Kitchen
	if len(f.extToggles) != 1 || f.extToggles[0] != [2]string{"shelly1-aaa", "true"} {
		t.Fatalf("EnableRangeExtender calls = %v", f.extToggles)
	}

	// Pair: extender = Kitchen, edge = Hall.
	m = press(t, m, "down", "enter") // pair flow
	mustContain(t, m.View(), "pick the extender")
	m = press(t, m, "enter") // extender = shelly1-aaa
	mustContain(t, m.View(), "edge device")
	m = press(t, m, "down", "enter") // edge = shelly1-bbb
	if len(f.joins) != 1 || f.joins[0] != [2]string{"shelly1-bbb", "shelly1-aaa"} {
		t.Fatalf("JoinExtender calls = %v", f.joins)
	}
	mustContain(t, m.View(), "shelly1-bbb joined extender shelly1-aaa")
}

func TestDeviceRemoveWithConfirm(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	m = press(t, m, "x")
	mustContain(t, m.View(), "Remove")
	m = press(t, m, "y")

	if len(f.removedKeys) != 1 || f.removedKeys[0] != "shelly1-aaa" {
		t.Fatalf("RemoveDevice keys = %v, want [shelly1-aaa]", f.removedKeys)
	}
	mustContain(t, m.View(), "removed device shelly1-aaa")
}

func TestQuitKeys(t *testing.T) {
	f := &fakeManager{devices: twoDevices()}
	m := newTestModel(t, f)

	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || func() bool { _, ok := cmd().(tea.QuitMsg); return !ok }() {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestWizardRequiresDevices(t *testing.T) {
	f := &fakeManager{}
	m := newTestModel(t, f)
	m = press(t, m, "2", "n")
	mustContain(t, m.View(), "no devices known")
}
