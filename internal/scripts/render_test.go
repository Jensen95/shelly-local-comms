package scripts

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

var update = flag.Bool("update", false, "regenerate golden files")

func representativeParams() Params {
	return Params{
		LinkID:          "kitchen-1",
		Name:            "Kitchen switch to hall light",
		SourceComponent: "input:0",
		SourceEvent:     "single_push",
		TargetAddr:      "192.168.1.42",
		TargetBLEMAC:    "AA:BB:CC:DD:EE:FF",
		TargetMethod:    "Switch.Toggle",
		TargetParams:    map[string]any{"id": 0},
		Fallback:        app.DefaultFallback(),
	}
}

func TestRenderGolden(t *testing.T) {
	got, err := Render(representativeParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	golden := filepath.Join("testdata", "link.js")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to regenerate): %v", err)
	}
	if got != string(want) {
		t.Errorf("rendered script differs from %s (run with -update to regenerate)\n--- got ---\n%s", golden, got)
	}
}

func TestRenderSize(t *testing.T) {
	got, err := Render(representativeParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(got) >= 5*1024 {
		t.Errorf("rendered script is %d bytes, must stay well under 5KB", len(got))
	}
}

func TestRenderValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Params)
		wantErr string
	}{
		{"missing LinkID", func(p *Params) { p.LinkID = "" }, "LinkID"},
		{"missing SourceComponent", func(p *Params) { p.SourceComponent = "" }, "SourceComponent"},
		{"missing SourceEvent", func(p *Params) { p.SourceEvent = "" }, "SourceEvent"},
		{"missing TargetAddr", func(p *Params) { p.TargetAddr = "" }, "TargetAddr"},
		{"missing TargetMethod", func(p *Params) { p.TargetMethod = "" }, "TargetMethod"},
		{"whitespace-only TargetAddr", func(p *Params) { p.TargetAddr = "  " }, "TargetAddr"},
		{
			"multiple missing listed together",
			func(p *Params) { p.LinkID = ""; p.TargetMethod = "" },
			"LinkID, TargetMethod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := representativeParams()
			tt.mutate(&p)
			_, err := Render(p)
			if err == nil {
				t.Fatalf("Render succeeded, want error mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestRenderValidParamsSucceed(t *testing.T) {
	// Name, TargetBLEMAC, TargetParams and Fallback are all optional.
	p := Params{
		LinkID:          "l1",
		SourceComponent: "input:1",
		SourceEvent:     "toggle",
		TargetAddr:      "10.0.0.9",
		TargetMethod:    "Switch.Set",
	}
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Zero fallback config falls back to defaults.
	for _, marker := range []string{"baseTimeoutMs: 400", "maxTimeoutMs: 1500", "latencyFactor: 4", "params: null"} {
		if !strings.Contains(got, marker) {
			t.Errorf("script missing %q", marker)
		}
	}
}

const (
	markerBLEDetect  = `typeof BLE !== "undefined" && BLE.RPC && typeof BLE.RPC.call === "function"`
	markerBLECall    = "BLE.RPC.call(CONFIG.bleMac, CONFIG.method, CONFIG.params"
	markerTimerRace  = "Timer.set(tmo, false, function ()"
	markerEWMAUpdate = "ewmaMs = 0.3 * rtt + 0.7 * ewmaMs;"
	markerEvent      = "event.component === CONFIG.srcComponent && event.info.event === CONFIG.srcEvent"
)

func TestRenderMarkersBLEEnabled(t *testing.T) {
	got, err := Render(representativeParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, m := range []string{markerBLEDetect, markerBLECall, markerTimerRace, markerEWMAUpdate, markerEvent, "bleEnabled: true"} {
		if !strings.Contains(got, m) {
			t.Errorf("BLE-enabled script missing marker %q", m)
		}
	}
}

func TestRenderMarkersBLEDisabled(t *testing.T) {
	p := representativeParams()
	p.Fallback.BLEEnabled = false
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, m := range []string{markerBLEDetect, markerBLECall, "bleEnabled: true"} {
		if strings.Contains(got, m) {
			t.Errorf("BLE-disabled script must not contain %q", m)
		}
	}
	// The two-tier timer race and EWMA logic stay regardless of BLE.
	for _, m := range []string{markerTimerRace, markerEWMAUpdate, markerEvent, "bleEnabled: false", "function bleFallback()"} {
		if !strings.Contains(got, m) {
			t.Errorf("BLE-disabled script missing marker %q", m)
		}
	}
}

func TestRenderMarkersBLEEnabledWithoutMAC(t *testing.T) {
	// BLE requested but no MAC known: the BLE tier must not be emitted.
	p := representativeParams()
	p.TargetBLEMAC = ""
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, markerBLECall) {
		t.Errorf("script without BLE MAC must not contain %q", markerBLECall)
	}
	if !strings.Contains(got, "bleEnabled: false") {
		t.Errorf("script without BLE MAC should declare bleEnabled: false")
	}
}

func TestRenderEscapesStrings(t *testing.T) {
	p := representativeParams()
	p.Name = "evil \"name\"\nsecond line"
	p.SourceEvent = `pu"sh`
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `srcEvent: "pu\"sh"`) {
		t.Errorf("srcEvent not JS-escaped:\n%s", got)
	}
	if strings.Contains(got, "\nsecond line") {
		t.Errorf("newline in Name leaked out of the header comment")
	}
}

func raceParams() Params {
	p := representativeParams()
	p.TargetMethod = "Switch.Set"
	p.TargetParams = map[string]any{"id": 0, "on": true}
	p.Fallback.Strategy = app.StrategyRace
	return p
}

func TestRenderMarkersRace(t *testing.T) {
	got, err := Render(raceParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, m := range []string{markerBLEDetect, markerBLECall, markerEvent, `strategy: "race"`, "racing LAN + BLE"} {
		if !strings.Contains(got, m) {
			t.Errorf("race script missing marker %q", m)
		}
	}
	// Race mode fires both tiers immediately: no failover timer, no
	// bleFallback function, and no EWMA bookkeeping (nothing reads it).
	for _, m := range []string{markerTimerRace, "function bleFallback()", markerEWMAUpdate, "ewmaMs"} {
		if strings.Contains(got, m) {
			t.Errorf("race script must not contain %q", m)
		}
	}
	if len(got) >= 5*1024 {
		t.Errorf("race script is %d bytes, must stay well under 5KB", len(got))
	}
}

func TestRenderRaceRejectsNonIdempotentMethod(t *testing.T) {
	p := raceParams()
	p.TargetMethod = "Switch.Toggle"
	if _, err := Render(p); err == nil || !strings.Contains(err.Error(), "idempotent") {
		t.Fatalf("want idempotency error for race+Toggle, got %v", err)
	}
}

func TestRenderUnknownStrategy(t *testing.T) {
	p := representativeParams()
	p.Fallback.Strategy = "sometimes"
	if _, err := Render(p); err == nil || !strings.Contains(err.Error(), "unknown strategy") {
		t.Fatalf("want unknown-strategy error, got %v", err)
	}
}

func TestRenderRaceWithoutMACDowngradesToFallback(t *testing.T) {
	p := raceParams()
	p.TargetBLEMAC = ""
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, markerBLECall) || strings.Contains(got, "racing LAN + BLE") {
		t.Error("race without a BLE MAC must degrade to the LAN-only fallback script")
	}
	if !strings.Contains(got, markerTimerRace) {
		t.Errorf("degraded script missing fallback timer %q", markerTimerRace)
	}
	if !strings.Contains(got, `strategy: "fallback"`) {
		t.Error(`degraded script should declare strategy: "fallback"`)
	}
}
