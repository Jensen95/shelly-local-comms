package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

// fakeDevice is an httptest-backed Shelly Gen2 RPC endpoint.
type fakeDevice struct {
	t        *testing.T
	id       string
	calls    []string
	scriptID int
	running  bool
}

func (f *fakeDevice) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("decode rpc: %v", err)
		}
		f.calls = append(f.calls, req.Method)
		result := map[string]any{}
		switch req.Method {
		case "Shelly.GetDeviceInfo":
			result = map[string]any{"id": f.id, "model": "SNSW-001X16EU", "gen": 2, "app": "Switch"}
		case "Script.List":
			scripts := []map[string]any{}
			if f.scriptID != 0 {
				scripts = append(scripts, map[string]any{
					"id": f.scriptID, "name": "shellyctl-link-l1", "enable": true, "running": f.running,
				})
			}
			result = map[string]any{"scripts": scripts}
		case "Script.Create":
			f.scriptID = 7
			result = map[string]any{"id": f.scriptID}
		case "Script.Start":
			f.running = true
			result = map[string]any{"was_running": false}
		case "Script.GetStatus":
			result = map[string]any{"id": f.scriptID, "running": f.running}
		case "Script.Stop", "Script.PutCode", "Script.SetConfig", "Script.Delete":
			// generic ack
		case "WiFi.GetConfig":
			result = map[string]any{"ap": map[string]any{
				"ssid": f.id, "enable": false, "range_extender": map[string]any{"enable": false},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "src": f.id, "result": result})
	}
}

func newTestManager(t *testing.T) (*Manager, *fakeDevice) {
	t.Helper()
	src := &fakeDevice{t: t, id: "shelly-src"}
	tgt := &fakeDevice{t: t, id: "shelly-tgt"}
	srcSrv := httptest.NewServer(src.handler())
	tgtSrv := httptest.NewServer(tgt.handler())
	t.Cleanup(srcSrv.Close)
	t.Cleanup(tgtSrv.Close)

	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store)
	ctx := context.Background()
	if _, err := m.AddDevice(ctx, strings.TrimPrefix(srcSrv.URL, "http://"), ""); err != nil {
		t.Fatalf("add source: %v", err)
	}
	if _, err := m.AddDevice(ctx, strings.TrimPrefix(tgtSrv.URL, "http://"), ""); err != nil {
		t.Fatalf("add target: %v", err)
	}
	return m, src
}

func TestAddDeviceFillsInfo(t *testing.T) {
	m, _ := newTestManager(t)
	devs := m.Devices()
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}
	if devs[0].Info.ID != "shelly-src" || devs[0].Info.Gen != 2 {
		t.Fatalf("device info not filled: %+v", devs[0])
	}
}

func TestSaveLinkValidatesAndDefaults(t *testing.T) {
	m, _ := newTestManager(t)

	if _, err := m.SaveLink(app.Link{SourceDevice: "nope", TargetDevice: "shelly-tgt",
		SourceComponent: "input:0", SourceEvent: "toggle", TargetMethod: "Switch.Toggle"}); err == nil {
		t.Fatal("expected unknown source device error")
	}

	l, err := m.SaveLink(app.Link{SourceDevice: "shelly-src", TargetDevice: "shelly-tgt",
		SourceComponent: "input:0", SourceEvent: "toggle", TargetMethod: "Switch.Toggle"})
	if err != nil {
		t.Fatalf("save link: %v", err)
	}
	if l.ID == "" || l.Name == "" {
		t.Fatalf("id/name not defaulted: %+v", l)
	}
	if l.Fallback != app.DefaultFallback() {
		t.Fatalf("fallback not defaulted: %+v", l.Fallback)
	}
	if got := len(m.Links()); got != 1 {
		t.Fatalf("want 1 link, got %d", got)
	}
}

func TestDeployLinkPersistsScriptID(t *testing.T) {
	m, src := newTestManager(t)
	l, err := m.SaveLink(app.Link{ID: "l1", SourceDevice: "shelly-src", TargetDevice: "shelly-tgt",
		SourceComponent: "input:0", SourceEvent: "toggle", TargetMethod: "Switch.Toggle",
		TargetParams: map[string]any{"id": 0}})
	if err != nil {
		t.Fatal(err)
	}

	script, err := m.RenderLinkScript(l.ID)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(script, "Shelly.addEventHandler") {
		t.Fatalf("rendered script misses event handler:\n%s", script)
	}

	if err := m.DeployLink(context.Background(), l.ID); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if got := m.Links()[0].DeployedScriptID; got != 7 {
		t.Fatalf("want deployed script id 7, got %d", got)
	}
	joined := strings.Join(src.calls, ",")
	for _, want := range []string{"Script.Create", "Script.PutCode", "Script.Start", "Script.GetStatus"} {
		if !strings.Contains(joined, want) {
			t.Errorf("source device never saw %s (calls: %s)", want, joined)
		}
	}
}

func TestRemoveDeviceGuardedByLinks(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.SaveLink(app.Link{SourceDevice: "shelly-src", TargetDevice: "shelly-tgt",
		SourceComponent: "input:0", SourceEvent: "toggle", TargetMethod: "Switch.Toggle"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveDevice("shelly-tgt"); err == nil {
		t.Fatal("expected removal to be blocked by link reference")
	}
	links := m.Links()
	if err := m.DeleteLink(context.Background(), links[0].ID); err != nil {
		t.Fatalf("delete link: %v", err)
	}
	if err := m.RemoveDevice("shelly-tgt"); err != nil {
		t.Fatalf("remove after link delete: %v", err)
	}
	if got := len(m.Devices()); got != 1 {
		t.Fatalf("want 1 device left, got %d", got)
	}
}

func TestJoinExtenderRequiresActiveExtender(t *testing.T) {
	m, _ := newTestManager(t)
	err := m.JoinExtender(context.Background(), "shelly-src", "shelly-tgt")
	if err == nil || !strings.Contains(err.Error(), "not an active range extender") {
		t.Fatalf("expected inactive-extender error, got %v", err)
	}
}

func TestSaveLinkStrategyValidation(t *testing.T) {
	m, _ := newTestManager(t)
	base := app.Link{SourceDevice: "shelly-src", TargetDevice: "shelly-tgt",
		SourceComponent: "input:0", SourceEvent: "toggle"}

	race := base
	race.TargetMethod = "Switch.Toggle"
	race.Fallback = app.DefaultFallback()
	race.Fallback.Strategy = app.StrategyRace
	if _, err := m.SaveLink(race); err == nil || !strings.Contains(err.Error(), "idempotent") {
		t.Fatalf("want idempotency error for race+Toggle, got %v", err)
	}

	race.TargetMethod = "Switch.Set"
	race.TargetParams = map[string]any{"id": 0, "on": true}
	if _, err := m.SaveLink(race); err != nil {
		t.Fatalf("race with Switch.Set should save: %v", err)
	}

	bad := base
	bad.TargetMethod = "Switch.Set"
	bad.Fallback = app.DefaultFallback()
	bad.Fallback.Strategy = "sometimes"
	if _, err := m.SaveLink(bad); err == nil || !strings.Contains(err.Error(), "unknown link strategy") {
		t.Fatalf("want unknown-strategy error, got %v", err)
	}
}
