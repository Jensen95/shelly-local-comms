package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/d2d"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// fakeDevice is an httptest-backed Shelly Gen2 RPC endpoint.
type fakeDevice struct {
	t        *testing.T
	id       string
	calls    []string
	scriptID int
	running  bool
	rssi     int
	failMQTT bool
	// surveyJSON is what Script.Eval("getResults()") returns; "{}" when
	// unset (survey ran, saw nothing).
	surveyJSON string
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
		case "WiFi.GetStatus":
			result = map[string]any{"sta_ip": "192.0.2.1", "status": "got ip", "ssid": "home", "rssi": f.rssi}
		case "Script.Eval":
			sj := f.surveyJSON
			if sj == "" {
				sj = "{}"
			}
			result = map[string]any{"result": sj}
		case "MQTT.SetConfig":
			if f.failMQTT {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": req.ID, "src": f.id,
					"error": map[string]any{"code": -108, "message": "mqtt rejected"},
				})
				return
			}
			result = map[string]any{"restart_required": false}
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

func TestAutoDiscoverMergesDevices(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(10*time.Millisecond))
	sweeps := make(chan struct{}, 16)
	m.discoverFn = func(ctx context.Context, timeout time.Duration) ([]shelly.Device, error) {
		select {
		case sweeps <- struct{}{}:
		default:
		}
		return []shelly.Device{{
			Addr:   "192.0.2.99",
			Info:   shelly.DeviceInfo{ID: "shelly-auto", Gen: 2},
			Source: "mdns",
		}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.autoDiscover(ctx)

	for i := 0; i < 3; i++ {
		select {
		case <-sweeps:
		case <-time.After(2 * time.Second):
			t.Fatal("auto-discovery sweep did not run")
		}
	}

	devs := m.Devices()
	count := 0
	for _, d := range devs {
		if d.Key() == "shelly-auto" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly 1 auto-discovered device after repeated sweeps, got %d (devices: %+v)", count, devs)
	}
}

func TestDiscoveryDisabled(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	m.discoverFn = func(ctx context.Context, timeout time.Duration) ([]shelly.Device, error) {
		t.Error("discovery must not run when the interval is 0")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	time.Sleep(50 * time.Millisecond)
}

func TestDiscoverPreservesFullInfoOnSparseResult(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	full := shelly.Device{
		Addr: "192.0.2.10",
		Info: shelly.DeviceInfo{
			ID: "shelly-x", MAC: "AABBCCDDEEFF", Model: "SNSW-001X16EU",
			Gen: 2, Name: "Hall", AuthEnabled: true,
		},
		Source: "manual",
	}
	if err := store.Update(func(c *app.Config) error {
		c.Devices = append(c.Devices, full)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A sweep re-finds the device at a new address, but enrichment failed
	// (auth-protected): only mDNS TXT data, no MAC/model/auth flag.
	m.discoverFn = func(ctx context.Context, timeout time.Duration) ([]shelly.Device, error) {
		return []shelly.Device{{
			Addr:   "192.0.2.20",
			Info:   shelly.DeviceInfo{ID: "shelly-x", Gen: 2},
			Source: "mdns",
		}}, nil
	}
	if _, err := m.Discover(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	devs := m.Devices()
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	got := devs[0]
	if got.Addr != "192.0.2.20" {
		t.Errorf("Addr = %q, want updated 192.0.2.20", got.Addr)
	}
	if got.Source != "manual" {
		t.Errorf("Source = %q, want preserved manual", got.Source)
	}
	if !got.Info.AuthEnabled || got.Info.MAC != "AABBCCDDEEFF" || got.Info.Name != "Hall" {
		t.Errorf("full DeviceInfo was clobbered by sparse mDNS info: %+v", got.Info)
	}
}

func TestDiscoverClampsTimeout(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	var gotTimeout time.Duration
	m.discoverFn = func(ctx context.Context, timeout time.Duration) ([]shelly.Device, error) {
		gotTimeout = timeout
		return nil, nil
	}
	if _, err := m.Discover(context.Background(), 8640000); err != nil {
		t.Fatal(err)
	}
	if gotTimeout != maxDiscoverSeconds*time.Second {
		t.Fatalf("timeout = %v, want clamped to %ds", gotTimeout, maxDiscoverSeconds)
	}
}

func addFakeDevice(t *testing.T, m *Manager, id string, rssi int) *fakeDevice {
	t.Helper()
	f := &fakeDevice{t: t, id: id, rssi: rssi}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	if _, err := m.AddDevice(context.Background(), strings.TrimPrefix(srv.URL, "http://"), ""); err != nil {
		t.Fatalf("add %s: %v", id, err)
	}
	return f
}

func newSuggestionManager(t *testing.T) *Manager {
	t.Helper()
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	m.surveyWait = time.Millisecond
	return m
}

func TestSuggestExtendersPairsWeakWithStrongest(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-strong", -48)
	addFakeDevice(t, m, "shelly-mid", -62)
	addFakeDevice(t, m, "shelly-weak", -81)
	addFakeDevice(t, m, "shelly-weaker", -88)

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("SuggestExtenders: %v", err)
	}
	if len(sugg) != 2 {
		t.Fatalf("suggestions = %+v, want 2", sugg)
	}
	// Sorted worst signal first, both paired with the strongest device.
	if sugg[0].Edge != "shelly-weaker" || sugg[1].Edge != "shelly-weak" {
		t.Errorf("edge order = %s, %s; want shelly-weaker first", sugg[0].Edge, sugg[1].Edge)
	}
	for _, s := range sugg {
		if s.Extender != "shelly-strong" {
			t.Errorf("suggested extender for %s = %s, want shelly-strong", s.Edge, s.Extender)
		}
		if s.Method != app.SuggestWiFiFallback {
			t.Errorf("method = %q, want %q (empty survey must fall back)", s.Method, app.SuggestWiFiFallback)
		}
	}
}

func TestSuggestExtendersNoWeakDevices(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-a", -50)
	addFakeDevice(t, m, "shelly-b", -60)

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("SuggestExtenders: %v", err)
	}
	if len(sugg) != 0 {
		t.Fatalf("suggestions = %+v, want none", sugg)
	}
}

func TestMergeAnnouncedUpdatesAddrOnly(t *testing.T) {
	m, _ := newTestManager(t)
	before, _ := m.deviceByKey("shelly-tgt")

	m.mergeAnnounced(shelly.Device{
		Addr:   "192.0.2.77",
		Source: "mqtt",
		Info:   shelly.DeviceInfo{ID: "shelly-tgt", Gen: 2, MAC: "FFEEDD"},
	})

	after, ok := m.deviceByKey("shelly-tgt")
	if !ok {
		t.Fatal("device vanished after announce merge")
	}
	if after.Addr != "192.0.2.77" {
		t.Errorf("Addr = %q, want announce address", after.Addr)
	}
	if after.Info != before.Info || after.Source != before.Source {
		t.Errorf("announce merge must only touch Addr; before %+v after %+v", before, after)
	}
	if got := len(m.Devices()); got != 2 {
		t.Fatalf("device count = %d, want 2", got)
	}
}

func TestMergeAnnouncedEnrichesNewDevice(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	f := &fakeDevice{t: t, id: "shelly-mq"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	m.mergeAnnounced(shelly.Device{
		Addr:   strings.TrimPrefix(srv.URL, "http://"),
		Source: "mqtt",
		Info:   shelly.DeviceInfo{ID: "shelly-mq", Gen: 2},
	})

	devs := m.Devices()
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	if devs[0].Info.Model != "SNSW-001X16EU" {
		t.Errorf("announced device was not enriched over HTTP: %+v", devs[0].Info)
	}
	if devs[0].Source != "mqtt" {
		t.Errorf("Source = %q, want mqtt", devs[0].Source)
	}
}

func TestSuggestExtendersIgnoresZeroRSSI(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-eth", 0) // Ethernet/AP-only: no WiFi uplink
	addFakeDevice(t, m, "shelly-mid", -62)
	addFakeDevice(t, m, "shelly-weak", -80)

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("SuggestExtenders: %v", err)
	}
	if len(sugg) != 1 || sugg[0].Extender != "shelly-mid" {
		t.Fatalf("suggestions = %+v, want weak paired with shelly-mid (never the rssi-0 device)", sugg)
	}
}

func TestApplyMQTTPersistsOnlyWhenAccepted(t *testing.T) {
	store, err := app.OpenStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(store, WithDiscoveryInterval(0))
	f := &fakeDevice{t: t, id: "shelly-a", failMQTT: true}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	if _, err := m.AddDevice(context.Background(), strings.TrimPrefix(srv.URL, "http://"), ""); err != nil {
		t.Fatal(err)
	}

	s := app.MQTTSettings{Enable: true, Server: "broker.bad:1883"}
	if err := m.ApplyMQTT(context.Background(), s, nil); err == nil {
		t.Fatal("expected apply error from rejecting device")
	}
	if got := m.store.Config().MQTT.Server; got != "" {
		t.Fatalf("rejected broker was persisted: %q", got)
	}

	f.failMQTT = false
	if err := m.ApplyMQTT(context.Background(), s, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := m.store.Config().MQTT.Server; got != "broker.bad:1883" {
		t.Fatalf("accepted broker not persisted: %q", got)
	}
}

func TestSuggestExtendersPrefersBLEProximity(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-strong", -48)
	addFakeDevice(t, m, "shelly-mid", -62)
	weak := addFakeDevice(t, m, "shelly-weak", -81)
	// The weak device hears shelly-mid loud and clear right next to it,
	// while the strongest-wifi device is barely audible (far away).
	weak.surveyJSON = `{
		"aa:bb:cc:dd:ee:01": {"rssi": -45, "count": 12, "name": "shelly-mid"},
		"aa:bb:cc:dd:ee:02": {"rssi": -87, "count": 3,  "name": "shelly-strong"}
	}`

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("SuggestExtenders: %v", err)
	}
	if len(sugg) != 1 {
		t.Fatalf("suggestions = %+v, want 1", sugg)
	}
	s := sugg[0]
	if s.Extender != "shelly-mid" {
		t.Fatalf("extender = %s, want the closest device shelly-mid (not the strongest-wifi one): %+v", s.Extender, s)
	}
	if s.Method != app.SuggestBLEProximity || s.BLERSSI != -45 {
		t.Fatalf("method/blerssi = %q/%d, want %q/-45", s.Method, s.BLERSSI, app.SuggestBLEProximity)
	}
}

func TestSuggestExtendersSkipsWeakNeighbors(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-strong", -48)
	addFakeDevice(t, m, "shelly-alsoweak", -78)
	weak := addFakeDevice(t, m, "shelly-weak", -81)
	// The closest neighbor by BLE is itself weak — useless as an
	// extender; the next match (strong) should win.
	weak.surveyJSON = `{
		"aa:bb:cc:dd:ee:01": {"rssi": -40, "count": 12, "name": "shelly-alsoweak"},
		"aa:bb:cc:dd:ee:02": {"rssi": -70, "count": 5,  "name": "shelly-strong"}
	}`

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("SuggestExtenders: %v", err)
	}
	// shelly-alsoweak also gets its own suggestion; find shelly-weak's.
	var s *app.ExtenderSuggestion
	for i := range sugg {
		if sugg[i].Edge == "shelly-weak" {
			s = &sugg[i]
		}
	}
	if s == nil {
		t.Fatalf("no suggestion for shelly-weak: %+v", sugg)
	}
	if s.Extender != "shelly-strong" || s.Method != app.SuggestBLEProximity {
		t.Fatalf("suggestion = %+v, want shelly-strong via ble-proximity (weak neighbor skipped)", s)
	}
}

func TestMatchSurveyByMACAdjacency(t *testing.T) {
	devices := []shelly.Device{
		{Addr: "192.0.2.5", Info: shelly.DeviceInfo{ID: "shellyplus1-x", MAC: "A8032AB12340"}},
	}
	seen := map[string]d2d.SurveyEntry{
		// BLE MAC = WiFi MAC + 2, scanner formatting with colons and
		// address-type suffix.
		"a8:03:2a:b1:23:42 1": {RSSI: -55, Count: 4},
	}
	got := matchSurvey(seen, devices, "other")
	if len(got) != 1 || got[0].key != "shellyplus1-x" || got[0].bleRSSI != -55 {
		t.Fatalf("matchSurvey = %+v", got)
	}
	// Distance 3 must not match.
	seen = map[string]d2d.SurveyEntry{"a8:03:2a:b1:23:43 1": {RSSI: -55, Count: 4}}
	if got := matchSurvey(seen, devices, "other"); len(got) != 0 {
		t.Fatalf("MAC three apart matched: %+v", got)
	}
}

func TestMatchSurveyAmbiguousAdjacencyMatchesNothing(t *testing.T) {
	devices := []shelly.Device{
		{Addr: "192.0.2.5", Info: shelly.DeviceInfo{ID: "shelly-a", MAC: "A8032AB12340"}},
		{Addr: "192.0.2.6", Info: shelly.DeviceInfo{ID: "shelly-b", MAC: "A8032AB12344"}},
	}
	// ...42 is exactly 2 away from both devices: crediting either would be
	// a guess presented as a proximity measurement.
	seen := map[string]d2d.SurveyEntry{"a8:03:2a:b1:23:42": {RSSI: -50, Count: 3}}
	if got := matchSurvey(seen, devices, "other"); len(got) != 0 {
		t.Fatalf("ambiguous adjacency matched: %+v", got)
	}
}

func TestMatchSurveyExactBLEMACWins(t *testing.T) {
	devices := []shelly.Device{
		{Addr: "192.0.2.5", BLEMAC: "a8:03:2a:b1:23:42", Info: shelly.DeviceInfo{ID: "shelly-a", MAC: "A8032AB12340"}},
		{Addr: "192.0.2.6", Info: shelly.DeviceInfo{ID: "shelly-b", MAC: "A8032AB12344"}},
	}
	// Same ambiguous address as above, but shelly-a's stored BLE MAC
	// resolves it deterministically.
	seen := map[string]d2d.SurveyEntry{"a8:03:2a:b1:23:42": {RSSI: -50, Count: 3}}
	got := matchSurvey(seen, devices, "other")
	if len(got) != 1 || got[0].key != "shelly-a" {
		t.Fatalf("matchSurvey = %+v, want exact BLEMAC match on shelly-a", got)
	}
}

func TestSuggestExtendersAllWiredIsNoSuggestionsNotError(t *testing.T) {
	m := newSuggestionManager(t)
	addFakeDevice(t, m, "shelly-eth1", 0)
	addFakeDevice(t, m, "shelly-eth2", 0)

	sugg, err := m.SuggestExtenders(context.Background())
	if err != nil {
		t.Fatalf("all-wired fleet must not be an error, got %v", err)
	}
	if len(sugg) != 0 {
		t.Fatalf("suggestions = %+v, want none", sugg)
	}
}
