package d2d

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// --- fake caller -----------------------------------------------------------

type recordedCall struct {
	method string
	params map[string]any // params after a JSON round-trip; nil when absent
}

type stub struct {
	method string // expected RPC method for this step
	result string // JSON unmarshaled into out (when out != nil)
	err    error  // returned instead of result when non-nil
}

// fakeCaller plays back a scripted sequence of RPC responses and records
// every (method, params) it sees.
type fakeCaller struct {
	t     *testing.T
	stubs []stub
	calls []recordedCall
}

func (f *fakeCaller) Call(_ context.Context, method string, params any, out any) error {
	f.t.Helper()
	var decoded map[string]any
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			f.t.Fatalf("marshal params of %s: %v", method, err)
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			f.t.Fatalf("params of %s are not an object: %v", method, err)
		}
	}
	f.calls = append(f.calls, recordedCall{method: method, params: decoded})

	if len(f.stubs) == 0 {
		f.t.Fatalf("unexpected extra call %s", method)
	}
	s := f.stubs[0]
	f.stubs = f.stubs[1:]
	if s.method != method {
		f.t.Fatalf("call %d: got method %s, want %s", len(f.calls), method, s.method)
	}
	if s.err != nil {
		return s.err
	}
	if out != nil && s.result != "" {
		if err := json.Unmarshal([]byte(s.result), out); err != nil {
			f.t.Fatalf("unmarshal canned result for %s: %v", method, err)
		}
	}
	return nil
}

func (f *fakeCaller) methods() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.method
	}
	return out
}

func deployerFor(t *testing.T, wantAddr string, f *fakeCaller) *Deployer {
	t.Helper()
	return NewDeployer(func(addr string) shelly.Caller {
		if addr != wantAddr {
			t.Fatalf("callerFor called with %q, want %q", addr, wantAddr)
		}
		return f
	})
}

// --- fixtures ---------------------------------------------------------------

func testLink() app.Link {
	return app.Link{
		ID:              "abc123",
		Name:            "desk to lamp",
		SourceDevice:    "src-key",
		TargetDevice:    "tgt-key",
		SourceComponent: "input:0",
		SourceEvent:     "single_push",
		TargetMethod:    "Switch.Toggle",
		TargetParams:    map[string]any{"id": 0},
		Fallback:        app.DefaultFallback(),
	}
}

func testSource() shelly.Device { return shelly.Device{Addr: "10.0.0.1"} }

func testTarget() shelly.Device {
	return shelly.Device{Addr: "10.0.0.2", BLEMAC: "AA:BB:CC:DD:EE:FF"}
}

func notRunningErr() error {
	return &shelly.RPCError{Code: -103, Message: "script is not running"}
}

// putCodeStubs returns one PutCode stub per expected chunk of code.
func putCodeStubs(code string) []stub {
	var out []stub
	for i := 0; i < len(code); i += chunkSize {
		out = append(out, stub{method: "Script.PutCode", result: `{"len":1}`})
	}
	return out
}

// --- tests -------------------------------------------------------------------

func TestDeployFresh(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	d := &Deployer{} // Render needs no caller
	code, err := d.Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(code) <= chunkSize {
		t.Fatalf("test premise broken: rendered code is %d bytes, need > %d to exercise chunking", len(code), chunkSize)
	}

	stubs := []stub{
		{method: "Script.List", result: `{"scripts":[{"id":1,"name":"user-script"}]}`},
		{method: "Script.Create", result: `{"id":3}`},
		{method: "Script.Stop", err: notRunningErr()}, // fresh slot is not running; tolerated
	}
	stubs = append(stubs, putCodeStubs(code)...)
	stubs = append(stubs,
		stub{method: "Script.SetConfig", result: `{"restart_required":false}`},
		stub{method: "Script.Start", result: `{"was_running":false}`},
		stub{method: "Script.GetStatus", result: `{"running":true}`},
	)
	f := &fakeCaller{t: t, stubs: stubs}

	id, err := deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if id != 3 {
		t.Errorf("Deploy returned id %d, want 3", id)
	}
	if len(f.stubs) != 0 {
		t.Errorf("%d scripted responses were never consumed", len(f.stubs))
	}

	if got := f.calls[1].params["name"]; got != "shellyctl-link-abc123" {
		t.Errorf("Script.Create name = %v, want shellyctl-link-abc123", got)
	}

	// Every post-create call must address script id 3.
	for _, c := range f.calls[2:] {
		if got := c.params["id"]; got != float64(3) {
			t.Errorf("%s used id %v, want 3", c.method, got)
		}
	}
}

func TestDeployRedeployReusesSlot(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	code, err := (&Deployer{}).Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	stubs := []stub{
		{method: "Script.List", result: `{"scripts":[{"id":2,"name":"other"},{"id":7,"name":"shellyctl-link-abc123"}]}`},
		{method: "Script.Stop", result: `{"was_running":true}`}, // was running; clean stop
	}
	stubs = append(stubs, putCodeStubs(code)...)
	stubs = append(stubs,
		stub{method: "Script.SetConfig", result: `{}`},
		stub{method: "Script.Start", result: `{}`},
		stub{method: "Script.GetStatus", result: `{"running":true}`},
	)
	f := &fakeCaller{t: t, stubs: stubs}

	id, err := deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if id != 7 {
		t.Errorf("Deploy returned id %d, want existing slot 7", id)
	}
	for _, m := range f.methods() {
		if m == "Script.Create" {
			t.Errorf("redeploy must not call Script.Create; calls: %v", f.methods())
		}
	}
}

func TestDeployChunkedUploadBoundaries(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	// Inflate the params so the code spans several chunks.
	link.TargetParams = map[string]any{"id": 0, "note": strings.Repeat("x", 900)}
	code, err := (&Deployer{}).Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	wantChunks := (len(code) + chunkSize - 1) / chunkSize
	if wantChunks < 3 {
		t.Fatalf("test premise broken: want at least 3 chunks, got %d (%d bytes)", wantChunks, len(code))
	}

	stubs := []stub{
		{method: "Script.List", result: `{"scripts":[]}`},
		{method: "Script.Create", result: `{"id":5}`},
		{method: "Script.Stop", err: notRunningErr()},
	}
	stubs = append(stubs, putCodeStubs(code)...)
	stubs = append(stubs,
		stub{method: "Script.SetConfig", result: `{}`},
		stub{method: "Script.Start", result: `{}`},
		stub{method: "Script.GetStatus", result: `{"running":true}`},
	)
	f := &fakeCaller{t: t, stubs: stubs}

	if _, err := deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	var chunks []recordedCall
	for _, c := range f.calls {
		if c.method == "Script.PutCode" {
			chunks = append(chunks, c)
		}
	}
	if len(chunks) != wantChunks {
		t.Fatalf("got %d PutCode calls, want %d", len(chunks), wantChunks)
	}
	var rebuilt strings.Builder
	for i, c := range chunks {
		chunk, ok := c.params["code"].(string)
		if !ok {
			t.Fatalf("chunk %d: code param is %T, want string", i, c.params["code"])
		}
		if len(chunk) == 0 || len(chunk) > chunkSize {
			t.Errorf("chunk %d is %d bytes, want 1..%d", i, len(chunk), chunkSize)
		}
		if i < len(chunks)-1 && len(chunk) != chunkSize {
			t.Errorf("non-final chunk %d is %d bytes, want exactly %d", i, len(chunk), chunkSize)
		}
		wantAppend := i > 0
		if got := c.params["append"]; got != wantAppend {
			t.Errorf("chunk %d: append = %v, want %v", i, got, wantAppend)
		}
		rebuilt.WriteString(chunk)
	}
	if rebuilt.String() != code {
		t.Errorf("concatenated chunks do not reproduce the rendered code")
	}
}

func TestChunkEndDoesNotSplitUTF8(t *testing.T) {
	// Place a multi-byte rune straddling the chunk boundary.
	code := strings.Repeat("a", chunkSize-1) + "æ" + strings.Repeat("b", 10)
	end := chunkEnd(code, 0)
	if end != chunkSize-1 {
		t.Errorf("chunkEnd = %d, want %d (backed off before the 2-byte rune)", end, chunkSize-1)
	}
	if !strings.HasPrefix(code[end:], "æ") {
		t.Errorf("second chunk does not start with the intact rune")
	}
}

func TestDeployStartVerificationFailure(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	code, err := (&Deployer{}).Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	stubs := []stub{
		{method: "Script.List", result: `{"scripts":[]}`},
		{method: "Script.Create", result: `{"id":4}`},
		{method: "Script.Stop", err: notRunningErr()},
	}
	stubs = append(stubs, putCodeStubs(code)...)
	stubs = append(stubs,
		stub{method: "Script.SetConfig", result: `{}`},
		stub{method: "Script.Start", result: `{}`},
		stub{method: "Script.GetStatus", result: `{"running":false}`},
	)
	f := &fakeCaller{t: t, stubs: stubs}

	_, err = deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target)
	if err == nil {
		t.Fatal("Deploy succeeded, want start-verification error")
	}
	if !strings.Contains(err.Error(), "did not start") {
		t.Errorf("error %q does not mention start failure", err)
	}
}

func TestDeployStopTransportErrorPropagates(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	f := &fakeCaller{t: t, stubs: []stub{
		{method: "Script.List", result: `{"scripts":[]}`},
		{method: "Script.Create", result: `{"id":4}`},
		{method: "Script.Stop", err: errors.New("dial tcp: connection refused")},
	}}
	_, err := deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Deploy err = %v, want propagated transport error", err)
	}
}

func TestDeployRefusesAuthEnabledTarget(t *testing.T) {
	link, source, target := testLink(), testSource(), testTarget()
	target.Info.AuthEnabled = true
	f := &fakeCaller{t: t} // no stubs: no RPC may happen
	_, err := deployerFor(t, source.Addr, f).Deploy(context.Background(), source, link, target)
	if err == nil {
		t.Fatal("Deploy succeeded against auth-enabled target, want error")
	}
	if !strings.Contains(err.Error(), "digest auth") {
		t.Errorf("error %q does not explain the digest-auth limitation", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("Deploy made %d RPC calls before failing on auth check: %v", len(f.calls), f.methods())
	}
}

func TestUndeploy(t *testing.T) {
	source := testSource()
	f := &fakeCaller{t: t, stubs: []stub{
		{method: "Script.Stop", err: notRunningErr()}, // tolerated
		{method: "Script.Delete", result: "null"},
	}}
	if err := deployerFor(t, source.Addr, f).Undeploy(context.Background(), source, 9); err != nil {
		t.Fatalf("Undeploy: %v", err)
	}
	want := []string{"Script.Stop", "Script.Delete"}
	got := f.methods()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("calls = %v, want %v", got, want)
	}
	for _, c := range f.calls {
		if c.params["id"] != float64(9) {
			t.Errorf("%s used id %v, want 9", c.method, c.params["id"])
		}
	}
}

func TestUndeployDeleteErrorPropagates(t *testing.T) {
	source := testSource()
	f := &fakeCaller{t: t, stubs: []stub{
		{method: "Script.Stop", result: "null"},
		{method: "Script.Delete", err: &shelly.RPCError{Code: -105, Message: "no such script"}},
	}}
	err := deployerFor(t, source.Addr, f).Undeploy(context.Background(), source, 9)
	if err == nil || !strings.Contains(err.Error(), "no such script") {
		t.Fatalf("Undeploy err = %v, want delete error", err)
	}
}

func TestRenderDowngradesBLEWithoutMAC(t *testing.T) {
	link, target := testLink(), testTarget()
	target.BLEMAC = "" // BLE requested by the link but MAC unknown
	got, err := (&Deployer{}).Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "BLE.RPC.call") {
		t.Errorf("script must not contain the BLE tier when the target MAC is unknown")
	}
	if !strings.Contains(got, "bleEnabled: false") {
		t.Errorf("script should declare bleEnabled: false after the downgrade")
	}
}

func TestRenderMapsLinkToScript(t *testing.T) {
	link, target := testLink(), testTarget()
	got, err := (&Deployer{}).Render(link, target)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, marker := range []string{
		`linkId: "abc123"`,
		`srcComponent: "input:0"`,
		`srcEvent: "single_push"`,
		`targetIp: "10.0.0.2"`,
		`method: "Switch.Toggle"`,
		`bleMac: "AA:BB:CC:DD:EE:FF"`,
		`bleEnabled: true`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("rendered script missing %q", marker)
		}
	}
}
