package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// fakeManager implements app.Manager, recording calls and returning canned
// data or injected errors.
type fakeManager struct {
	devices []shelly.Device
	links   []app.Link
	stats   []app.LatencyStats
	script  string

	err error // returned from every fallible method when set

	calls []string

	addedAddr       string
	addedPassword   string
	removedKey      string
	discoverTimeout int
	savedLink       app.Link
	deletedLinkID   string
	deployedLinkID  string
	renderedLinkID  string
	mqttSettings    app.MQTTSettings
	mqttKeys        []string
	bleSettings     app.BLESettings
	bleKeys         []string
	extenderKey     string
	extenderEnable  bool
	joinEdgeKey     string
	joinExtenderKey string
	suggestions     []app.ExtenderSuggestion
}

func (f *fakeManager) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeManager) Devices() []shelly.Device {
	f.record("Devices")
	return f.devices
}

func (f *fakeManager) Discover(ctx context.Context, timeoutSeconds int) ([]shelly.Device, error) {
	f.record("Discover")
	f.discoverTimeout = timeoutSeconds
	return f.devices, f.err
}

func (f *fakeManager) AddDevice(ctx context.Context, addr, password string) (shelly.Device, error) {
	f.record("AddDevice")
	f.addedAddr, f.addedPassword = addr, password
	if f.err != nil {
		return shelly.Device{}, f.err
	}
	return shelly.Device{Addr: addr, Source: "manual"}, nil
}

func (f *fakeManager) RemoveDevice(key string) error {
	f.record("RemoveDevice")
	f.removedKey = key
	return f.err
}

func (f *fakeManager) Links() []app.Link {
	f.record("Links")
	return f.links
}

func (f *fakeManager) SaveLink(l app.Link) (app.Link, error) {
	f.record("SaveLink")
	f.savedLink = l
	if f.err != nil {
		return app.Link{}, f.err
	}
	if l.ID == "" {
		l.ID = "generated-id"
	}
	return l, nil
}

func (f *fakeManager) DeleteLink(ctx context.Context, id string) error {
	f.record("DeleteLink")
	f.deletedLinkID = id
	return f.err
}

func (f *fakeManager) DeployLink(ctx context.Context, id string) error {
	f.record("DeployLink")
	f.deployedLinkID = id
	return f.err
}

func (f *fakeManager) RenderLinkScript(id string) (string, error) {
	f.record("RenderLinkScript")
	f.renderedLinkID = id
	if f.err != nil {
		return "", f.err
	}
	return f.script, nil
}

func (f *fakeManager) ApplyMQTT(ctx context.Context, s app.MQTTSettings, keys []string) error {
	f.record("ApplyMQTT")
	f.mqttSettings, f.mqttKeys = s, keys
	return f.err
}

func (f *fakeManager) ApplyBLE(ctx context.Context, s app.BLESettings, keys []string) error {
	f.record("ApplyBLE")
	f.bleSettings, f.bleKeys = s, keys
	return f.err
}

func (f *fakeManager) EnableRangeExtender(ctx context.Context, key string, enable bool) error {
	f.record("EnableRangeExtender")
	f.extenderKey, f.extenderEnable = key, enable
	return f.err
}

func (f *fakeManager) JoinExtender(ctx context.Context, edgeKey, extenderKey string) error {
	f.record("JoinExtender")
	f.joinEdgeKey, f.joinExtenderKey = edgeKey, extenderKey
	return f.err
}

func (f *fakeManager) SuggestExtenders(ctx context.Context) ([]app.ExtenderSuggestion, error) {
	f.record("SuggestExtenders")
	if f.err != nil {
		return nil, f.err
	}
	return f.suggestions, nil
}

func (f *fakeManager) LatencyStats() []app.LatencyStats {
	f.record("LatencyStats")
	return f.stats
}

func (f *fakeManager) ProbeNow(ctx context.Context) []app.LatencyStats {
	f.record("ProbeNow")
	return f.stats
}

var _ app.Manager = (*fakeManager)(nil)

func newTestServer(t *testing.T, f *fakeManager) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewServer(f))
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func decodeBody[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

func wantStatus(t *testing.T, res *http.Response, want int) {
	t.Helper()
	if res.StatusCode != want {
		t.Fatalf("status = %d, want %d", res.StatusCode, want)
	}
}

func wantJSONError(t *testing.T, res *http.Response) string {
	t.Helper()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("error content-type = %q, want application/json", ct)
	}
	body := decodeBody[map[string]string](t, res)
	if body["error"] == "" {
		t.Fatalf("error body missing \"error\" field: %v", body)
	}
	return body["error"]
}

var testDevices = []shelly.Device{
	{Addr: "192.168.1.10", Info: shelly.DeviceInfo{ID: "shellyplus1-a", Model: "SNSW-001X16EU", Gen: 2, Name: "Hall"}},
	{Addr: "192.168.1.11", Info: shelly.DeviceInfo{ID: "shellyplus1-b", Model: "SNSW-001X16EU", Gen: 2}},
}

// --- Devices ---

func TestListDevices(t *testing.T) {
	f := &fakeManager{devices: testDevices}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodGet, "/api/devices", "")
	wantStatus(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	got := decodeBody[[]shelly.Device](t, res)
	if len(got) != 2 || got[0].Info.ID != "shellyplus1-a" {
		t.Fatalf("devices = %+v", got)
	}
}

func TestListDevicesEmptyIsArray(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodGet, "/api/devices", "")
	wantStatus(t, res, http.StatusOK)
	var raw json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("body = %s, want []", raw)
	}
}

func TestAddDevice(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/devices", `{"addr":"192.168.1.20","password":"s3cret"}`)
	wantStatus(t, res, http.StatusCreated)
	dev := decodeBody[shelly.Device](t, res)
	if dev.Addr != "192.168.1.20" {
		t.Fatalf("device = %+v", dev)
	}
	if f.addedAddr != "192.168.1.20" || f.addedPassword != "s3cret" {
		t.Fatalf("AddDevice called with (%q, %q)", f.addedAddr, f.addedPassword)
	}
}

func TestAddDeviceInvalidJSON(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/devices", `{"addr":`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
	if len(f.calls) != 0 {
		t.Fatalf("manager called on invalid body: %v", f.calls)
	}
}

func TestAddDeviceMissingAddr(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodPost, "/api/devices", `{"password":"x"}`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
}

func TestRemoveDevice(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodDelete, "/api/devices/shellyplus1-a", "")
	wantStatus(t, res, http.StatusNoContent)
	if f.removedKey != "shellyplus1-a" {
		t.Fatalf("removedKey = %q", f.removedKey)
	}
}

func TestRemoveDeviceUnknownMapsTo404(t *testing.T) {
	f := &fakeManager{err: errors.New(`device "nope" not found`)}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodDelete, "/api/devices/nope", "")
	wantStatus(t, res, http.StatusNotFound)
	if msg := wantJSONError(t, res); !strings.Contains(msg, "not found") {
		t.Fatalf("error = %q", msg)
	}
}

// --- Discover ---

func TestDiscover(t *testing.T) {
	f := &fakeManager{devices: testDevices}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/discover", `{"timeout_seconds":2}`)
	wantStatus(t, res, http.StatusOK)
	got := decodeBody[[]shelly.Device](t, res)
	if len(got) != 2 {
		t.Fatalf("found = %+v", got)
	}
	if f.discoverTimeout != 2 {
		t.Fatalf("timeout = %d, want 2", f.discoverTimeout)
	}
}

func TestDiscoverDefaultTimeout(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/discover", "")
	wantStatus(t, res, http.StatusOK)
	if f.discoverTimeout != defaultDiscoverSeconds {
		t.Fatalf("timeout = %d, want %d", f.discoverTimeout, defaultDiscoverSeconds)
	}
}

func TestDiscoverNegativeTimeout(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodPost, "/api/discover", `{"timeout_seconds":-1}`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
}

func TestDiscoverError(t *testing.T) {
	f := &fakeManager{err: errors.New("mdns: socket busy")}
	ts := newTestServer(t, f)
	res := do(t, ts, http.MethodPost, "/api/discover", "{}")
	wantStatus(t, res, http.StatusInternalServerError)
	wantJSONError(t, res)
}

// --- Links ---

func TestListLinks(t *testing.T) {
	f := &fakeManager{links: []app.Link{{ID: "l1", Name: "hall"}}}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodGet, "/api/links", "")
	wantStatus(t, res, http.StatusOK)
	got := decodeBody[[]app.Link](t, res)
	if len(got) != 1 || got[0].ID != "l1" {
		t.Fatalf("links = %+v", got)
	}
}

func TestSaveLinkFillsDefaultFallback(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	body := `{"source_device":"a","target_device":"b","source_component":"input:0","source_event":"toggle","target_method":"Switch.Toggle"}`
	res := do(t, ts, http.MethodPost, "/api/links", body)
	wantStatus(t, res, http.StatusOK)
	saved := decodeBody[app.Link](t, res)
	if saved.ID != "generated-id" {
		t.Fatalf("id = %q", saved.ID)
	}
	if f.savedLink.Fallback != app.DefaultFallback() {
		t.Fatalf("fallback = %+v, want default", f.savedLink.Fallback)
	}
}

func TestSaveLinkKeepsExplicitFallback(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	body := `{"source_device":"a","target_device":"b","target_method":"Switch.Set","fallback":{"ble_enabled":false,"base_timeout_ms":200,"max_timeout_ms":900,"latency_factor":2}}`
	res := do(t, ts, http.MethodPost, "/api/links", body)
	wantStatus(t, res, http.StatusOK)
	want := app.FallbackConfig{BaseTimeoutMs: 200, MaxTimeoutMs: 900, LatencyFactor: 2}
	if f.savedLink.Fallback != want {
		t.Fatalf("fallback = %+v, want %+v", f.savedLink.Fallback, want)
	}
}

func TestSaveLinkInvalidJSON(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)
	res := do(t, ts, http.MethodPost, "/api/links", `not json`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
	if len(f.calls) != 0 {
		t.Fatalf("manager called: %v", f.calls)
	}
}

func TestSaveLinkMissingFields(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	for _, body := range []string{
		`{"target_device":"b","target_method":"Switch.Toggle"}`,
		`{"source_device":"a","target_method":"Switch.Toggle"}`,
		`{"source_device":"a","target_device":"b"}`,
	} {
		res := do(t, ts, http.MethodPost, "/api/links", body)
		wantStatus(t, res, http.StatusBadRequest)
		wantJSONError(t, res)
	}
}

func TestDeleteLink(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodDelete, "/api/links/l1", "")
	wantStatus(t, res, http.StatusNoContent)
	if f.deletedLinkID != "l1" {
		t.Fatalf("deletedLinkID = %q", f.deletedLinkID)
	}
}

func TestDeployLink(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/links/l1/deploy", "")
	wantStatus(t, res, http.StatusOK)
	if f.deployedLinkID != "l1" {
		t.Fatalf("deployedLinkID = %q", f.deployedLinkID)
	}
}

func TestDeployLinkErrorPropagates500(t *testing.T) {
	f := &fakeManager{err: errors.New("script upload failed: connection refused")}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/links/l1/deploy", "")
	wantStatus(t, res, http.StatusInternalServerError)
	if msg := wantJSONError(t, res); !strings.Contains(msg, "script upload failed") {
		t.Fatalf("error = %q", msg)
	}
}

func TestLinkScript(t *testing.T) {
	f := &fakeManager{script: "let TARGET = \"192.168.1.11\";\n"}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodGet, "/api/links/l1/script", "")
	wantStatus(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(b); got != f.script {
		t.Fatalf("script = %q, want %q", got, f.script)
	}
	if f.renderedLinkID != "l1" {
		t.Fatalf("renderedLinkID = %q", f.renderedLinkID)
	}
}

func TestLinkScriptUnknown404(t *testing.T) {
	f := &fakeManager{err: errors.New(`link "nope" not found`)}
	ts := newTestServer(t, f)
	res := do(t, ts, http.MethodGet, "/api/links/nope/script", "")
	wantStatus(t, res, http.StatusNotFound)
	wantJSONError(t, res)
}

// --- Health ---

func TestHealth(t *testing.T) {
	f := &fakeManager{stats: []app.LatencyStats{
		{Device: "shellyplus1-a", EWMA: 42 * time.Millisecond, Samples: 10},
		{Device: "shellyplus1-b", Degraded: true, Failures: 3},
	}}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodGet, "/api/health", "")
	wantStatus(t, res, http.StatusOK)
	got := decodeBody[[]app.LatencyStats](t, res)
	if len(got) != 2 || got[0].EWMA != 42*time.Millisecond || !got[1].Degraded {
		t.Fatalf("stats = %+v", got)
	}
}

func TestProbeNow(t *testing.T) {
	f := &fakeManager{stats: []app.LatencyStats{{Device: "x", Samples: 1}}}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/health/probe", "")
	wantStatus(t, res, http.StatusOK)
	got := decodeBody[[]app.LatencyStats](t, res)
	if len(got) != 1 || got[0].Device != "x" {
		t.Fatalf("stats = %+v", got)
	}
	if len(f.calls) != 1 || f.calls[0] != "ProbeNow" {
		t.Fatalf("calls = %v", f.calls)
	}
}

// --- Settings ---

func TestApplyMQTT(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	body := `{"settings":{"enable":true,"server":"192.168.1.2:1883","user":"u","pass":"p","rpc_ntf":true},"devices":["a","b"]}`
	res := do(t, ts, http.MethodPost, "/api/settings/mqtt", body)
	wantStatus(t, res, http.StatusOK)
	if !f.mqttSettings.Enable || f.mqttSettings.Server != "192.168.1.2:1883" || !f.mqttSettings.RPCNotifs {
		t.Fatalf("settings = %+v", f.mqttSettings)
	}
	if len(f.mqttKeys) != 2 || f.mqttKeys[0] != "a" {
		t.Fatalf("keys = %v", f.mqttKeys)
	}
}

func TestApplyMQTTEnableWithoutServer(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)
	res := do(t, ts, http.MethodPost, "/api/settings/mqtt", `{"settings":{"enable":true}}`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
	if len(f.calls) != 0 {
		t.Fatalf("manager called: %v", f.calls)
	}
}

func TestApplyBLE(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	body := `{"settings":{"enable":true,"rpc":true,"observer":false},"devices":[]}`
	res := do(t, ts, http.MethodPost, "/api/settings/ble", body)
	wantStatus(t, res, http.StatusOK)
	if !f.bleSettings.Enable || !f.bleSettings.RPC || f.bleSettings.Observer {
		t.Fatalf("settings = %+v", f.bleSettings)
	}
	if len(f.bleKeys) != 0 {
		t.Fatalf("keys = %v", f.bleKeys)
	}
}

func TestRangeExtender(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/settings/extender", `{"device":"shellyplus1-a","enable":true}`)
	wantStatus(t, res, http.StatusOK)
	if f.extenderKey != "shellyplus1-a" || !f.extenderEnable {
		t.Fatalf("EnableRangeExtender(%q, %v)", f.extenderKey, f.extenderEnable)
	}
}

func TestRangeExtenderMissingDevice(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodPost, "/api/settings/extender", `{"enable":true}`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
}

func TestJoinExtender(t *testing.T) {
	f := &fakeManager{}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodPost, "/api/settings/extender/join", `{"edge":"edge-1","extender":"ext-1"}`)
	wantStatus(t, res, http.StatusOK)
	if f.joinEdgeKey != "edge-1" || f.joinExtenderKey != "ext-1" {
		t.Fatalf("JoinExtender(%q, %q)", f.joinEdgeKey, f.joinExtenderKey)
	}
}

func TestJoinExtenderSameDevice(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodPost, "/api/settings/extender/join", `{"edge":"a","extender":"a"}`)
	wantStatus(t, res, http.StatusBadRequest)
	wantJSONError(t, res)
}

// --- Static UI & routing ---

func TestStaticIndexServedAtRoot(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})

	res := do(t, ts, http.MethodGet, "/", "")
	wantStatus(t, res, http.StatusOK)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	b := make([]byte, 4096)
	n, _ := res.Body.Read(b)
	if !strings.Contains(string(b[:n]), "shellyctl") {
		t.Fatalf("index.html does not mention shellyctl")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	for _, p := range []string{"/app.js", "/style.css"} {
		res := do(t, ts, http.MethodGet, p, "")
		wantStatus(t, res, http.StatusOK)
	}
}

func TestUnknownAPIPathIs404JSON(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})

	for _, p := range []string{"/api/nope", "/api/", "/api/devices/extra/segments"} {
		res := do(t, ts, http.MethodGet, p, "")
		wantStatus(t, res, http.StatusNotFound)
		wantJSONError(t, res)
	}
}

func TestUnknownStaticPathIs404(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodGet, "/definitely-not-here.html", "")
	wantStatus(t, res, http.StatusNotFound)
}

// --- Serve ---

func TestServeGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, "127.0.0.1:0", &fakeManager{}) }()

	// Give the server a moment to start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down")
	}
}

func TestServeBadAddr(t *testing.T) {
	err := Serve(context.Background(), "127.0.0.1:-1", &fakeManager{})
	if err == nil {
		t.Fatal("Serve with invalid addr returned nil error")
	}
}

func TestStateChangingPostRejectsNonJSONContentType(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/mqtt",
		strings.NewReader(`{"settings":{"enable":true,"server":"evil:1883"},"devices":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	// A cross-origin page can send text/plain without a CORS preflight;
	// the API must refuse it.
	req.Header.Set("Content-Type", "text/plain")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", res.StatusCode)
	}
}

func TestExtenderSuggestions(t *testing.T) {
	f := &fakeManager{suggestions: []app.ExtenderSuggestion{
		{Edge: "shelly-weak", EdgeRSSI: -82, Extender: "shelly-strong", ExtenderRSSI: -50},
	}}
	ts := newTestServer(t, f)

	res := do(t, ts, http.MethodGet, "/api/settings/extender/suggestions", "")
	wantStatus(t, res, http.StatusOK)
	got := decodeBody[[]app.ExtenderSuggestion](t, res)
	if len(got) != 1 || got[0].Edge != "shelly-weak" || got[0].Extender != "shelly-strong" {
		t.Fatalf("suggestions = %+v", got)
	}
}

func TestExtenderSuggestionsEmptyIsArray(t *testing.T) {
	ts := newTestServer(t, &fakeManager{})
	res := do(t, ts, http.MethodGet, "/api/settings/extender/suggestions", "")
	wantStatus(t, res, http.StatusOK)
	var raw json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("body = %s, want []", raw)
	}
}
