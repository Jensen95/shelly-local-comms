// Package scripts renders the mJS (Shelly embedded JavaScript) scripts
// that materialize device-to-device links. One script per link is
// deployed to the SOURCE device; it listens for the configured input
// event and calls the target device LAN-first (adaptive, EWMA-derived
// timeout) with an optional BLE RPC fallback tier.
package scripts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

// Params is everything needed to render one link script.
type Params struct {
	LinkID          string
	Name            string
	SourceComponent string
	SourceEvent     string
	TargetAddr      string
	TargetBLEMAC    string
	TargetMethod    string
	TargetParams    map[string]any
	Fallback        app.FallbackConfig
}

// Defaults applied when the corresponding FallbackConfig field is unset
// (zero). They mirror app.DefaultFallback.
const (
	defaultBaseTimeoutMs = 400
	defaultMaxTimeoutMs  = 1500
	defaultLatencyFactor = 4.0
)

// tmplData is the resolved, template-ready view of Params.
type tmplData struct {
	LinkID          string
	Name            string
	SourceComponent string
	SourceEvent     string
	TargetAddr      string
	TargetBLEMAC    string
	TargetMethod    string
	ParamsJSON      string
	// BLETier is true when the BLE fallback code should be emitted:
	// fallback enabled AND the target's BLE MAC is known.
	BLETier bool
	// Race is true when both transports fire simultaneously instead of
	// LAN-first-with-fallback. Only emitted when BLETier is also true —
	// with a single transport there is nothing to race.
	Race           bool
	BaseTimeoutMs  int
	MaxTimeoutMs   int
	LatencyFactor  float64
	HTTPTimeoutSec int
}

var funcs = template.FuncMap{
	// js renders a Go string as a quoted, escaped JS string literal.
	"js": func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			// json.Marshal of a string cannot fail; keep the template total.
			return `""`
		}
		return string(b)
	},
	// comment makes a string safe to embed in a // line comment.
	"comment": func(s string) string {
		s = strings.ReplaceAll(s, "\r", " ")
		return strings.ReplaceAll(s, "\n", " ")
	},
}

var scriptTmpl = template.Must(template.New("link").Funcs(funcs).Parse(scriptTemplate))

// Render produces the mJS link script for p. It validates required
// fields and applies default failover tuning for unset (zero) values.
func Render(p Params) (string, error) {
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"LinkID", p.LinkID},
		{"SourceComponent", p.SourceComponent},
		{"SourceEvent", p.SourceEvent},
		{"TargetAddr", p.TargetAddr},
		{"TargetMethod", p.TargetMethod},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("scripts: missing required fields: %s", strings.Join(missing, ", "))
	}
	switch p.Fallback.Strategy {
	case "", app.StrategyFallback:
	case app.StrategyRace:
		if app.NonIdempotentMethod(p.TargetMethod) {
			return "", fmt.Errorf("scripts: strategy %q requires an idempotent target method, but %q is toggle-style: both transports may deliver and the second call would undo the first — use %q or an explicit method like Switch.Set",
				app.StrategyRace, p.TargetMethod, app.StrategyFallback)
		}
	default:
		return "", fmt.Errorf("scripts: unknown strategy %q (want %q or %q)", p.Fallback.Strategy, app.StrategyFallback, app.StrategyRace)
	}

	paramsJSON := "null"
	if p.TargetParams != nil {
		b, err := json.Marshal(p.TargetParams)
		if err != nil {
			return "", fmt.Errorf("scripts: marshal target params: %w", err)
		}
		paramsJSON = string(b)
	}

	fb := p.Fallback
	if fb.BaseTimeoutMs <= 0 {
		fb.BaseTimeoutMs = defaultBaseTimeoutMs
	}
	if fb.MaxTimeoutMs <= 0 {
		fb.MaxTimeoutMs = defaultMaxTimeoutMs
	}
	if fb.MaxTimeoutMs < fb.BaseTimeoutMs {
		fb.MaxTimeoutMs = fb.BaseTimeoutMs
	}
	if fb.LatencyFactor <= 0 {
		fb.LatencyFactor = defaultLatencyFactor
	}

	name := p.Name
	if name == "" {
		name = p.LinkID
	}

	data := tmplData{
		LinkID:          p.LinkID,
		Name:            name,
		SourceComponent: p.SourceComponent,
		SourceEvent:     p.SourceEvent,
		TargetAddr:      p.TargetAddr,
		TargetBLEMAC:    p.TargetBLEMAC,
		TargetMethod:    p.TargetMethod,
		ParamsJSON:      paramsJSON,
		BLETier:         fb.BLEEnabled && p.TargetBLEMAC != "",
		Race:            fb.Strategy == app.StrategyRace && fb.BLEEnabled && p.TargetBLEMAC != "",
		BaseTimeoutMs:   fb.BaseTimeoutMs,
		MaxTimeoutMs:    fb.MaxTimeoutMs,
		LatencyFactor:   fb.LatencyFactor,
		// Shelly's HTTP.* timeout only has 1-second granularity; make it a
		// backstop strictly above the millisecond Timer that does the real
		// failover, so the Timer always wins the race on a slow LAN.
		HTTPTimeoutSec: (fb.MaxTimeoutMs+999)/1000 + 1,
	}

	var buf bytes.Buffer
	if err := scriptTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("scripts: render link %s: %w", p.LinkID, err)
	}
	return buf.String(), nil
}

// scriptTemplate is the mJS source. Kept ASCII-only and compact: the
// rendered output must stay well under Shelly's per-script size limits.
const scriptTemplate = `// Generated by shellyctl -- DO NOT EDIT.
// Link: {{comment .Name}} (id {{comment .LinkID}})
// Manual edits will be overwritten the next time this link is deployed.

let CONFIG = {
  linkId: {{js .LinkID}},
  srcComponent: {{js .SourceComponent}},
  srcEvent: {{js .SourceEvent}},
  targetIp: {{js .TargetAddr}},
  method: {{js .TargetMethod}},
  params: {{.ParamsJSON}},
  strategy: {{if .Race}}"race"{{else}}"fallback"{{end}},
  bleEnabled: {{.BLETier}},
  bleMac: {{js .TargetBLEMAC}},
  baseTimeoutMs: {{.BaseTimeoutMs}},
  maxTimeoutMs: {{.MaxTimeoutMs}},
  latencyFactor: {{.LatencyFactor}},
  httpTimeoutSec: {{.HTTPTimeoutSec}}
};

let attempt = 0; // per-attempt token: late callbacks from older attempts are ignored
{{if .Race}}
// Race strategy: the target action is idempotent, so LAN and BLE fire
// simultaneously; the faster path wins and the duplicate is harmless.
function onTrigger() {
  attempt++;
  let token = attempt;
  let winner = "";
  let start = Date.now();
  print("shellyctl[" + CONFIG.linkId + "]: trigger " + CONFIG.srcComponent + "/" + CONFIG.srcEvent + ", racing LAN + BLE");
  Shelly.call("HTTP.POST", {
    url: "http://" + CONFIG.targetIp + "/rpc",
    body: JSON.stringify({ id: 1, src: "shellyctl-link", method: CONFIG.method, params: CONFIG.params }),
    timeout: CONFIG.httpTimeoutSec
  }, function (res, err_code, err_msg) {
    if (token !== attempt) return;
    let rtt = Date.now() - start;
    if (err_code === 0 && res && res.code === 200) {
      if (winner === "") winner = "lan";
      print("shellyctl[" + CONFIG.linkId + "]: LAN ok in " + rtt + "ms" + (winner === "lan" ? " (won)" : ""));
    } else {
      print("shellyctl[" + CONFIG.linkId + "]: LAN failed after " + rtt + "ms: " + (err_msg ? err_msg : "HTTP " + (res ? JSON.stringify(res.code) : "?")));
    }
  });
  if (typeof BLE !== "undefined" && BLE.RPC && typeof BLE.RPC.call === "function") {
    BLE.RPC.call(CONFIG.bleMac, CONFIG.method, CONFIG.params, function (res, err_code, err_msg) {
      if (token !== attempt) return;
      let rtt = Date.now() - start;
      if (err_code === 0) {
        if (winner === "") winner = "ble";
        print("shellyctl[" + CONFIG.linkId + "]: BLE ok in " + rtt + "ms" + (winner === "ble" ? " (won)" : ""));
      } else {
        print("shellyctl[" + CONFIG.linkId + "]: BLE failed: " + err_msg);
      }
    });
  } else {
    print("shellyctl[" + CONFIG.linkId + "]: this firmware does not expose BLE RPC to scripts; race degrades to LAN only");
  }
}
{{else}}
let ewmaMs = 0;  // EWMA of successful LAN round-trips (ms); 0 = no samples yet

function recordRtt(rtt) {
  if (ewmaMs <= 0) {
    ewmaMs = rtt;
  } else {
    ewmaMs = 0.3 * rtt + 0.7 * ewmaMs;
  }
}

function adaptiveTimeoutMs() {
  if (ewmaMs <= 0) return CONFIG.baseTimeoutMs;
  let t = ewmaMs * CONFIG.latencyFactor;
  if (t < CONFIG.baseTimeoutMs) t = CONFIG.baseTimeoutMs;
  if (t > CONFIG.maxTimeoutMs) t = CONFIG.maxTimeoutMs;
  return t;
}
{{if .BLETier}}
function bleFallback() {
  if (typeof BLE !== "undefined" && BLE.RPC && typeof BLE.RPC.call === "function") {
    print("shellyctl[" + CONFIG.linkId + "]: falling back to BLE RPC " + CONFIG.bleMac);
    BLE.RPC.call(CONFIG.bleMac, CONFIG.method, CONFIG.params, function (res, err_code, err_msg) {
      if (err_code === 0) {
        print("shellyctl[" + CONFIG.linkId + "]: BLE ok");
      } else {
        print("shellyctl[" + CONFIG.linkId + "]: BLE failed: " + err_msg);
      }
    });
  } else {
    print("shellyctl[" + CONFIG.linkId + "]: this firmware does not expose BLE RPC to scripts; cannot fall back");
  }
}
{{else}}
function bleFallback() {
  print("shellyctl[" + CONFIG.linkId + "]: BLE fallback disabled; giving up");
}
{{end}}
function onTrigger() {
  attempt++;
  let token = attempt;
  let done = false;
  let start = Date.now();
  let tmo = adaptiveTimeoutMs();
  print("shellyctl[" + CONFIG.linkId + "]: trigger " + CONFIG.srcComponent + "/" + CONFIG.srcEvent + ", LAN timeout " + tmo + "ms");
  // The HTTP timeout below only has 1s granularity, so this Timer races the
  // RPC callback for millisecond-granularity failover; first to fire wins.
  Timer.set(tmo, false, function () {
    if (done || token !== attempt) return;
    done = true;
    print("shellyctl[" + CONFIG.linkId + "]: LAN timed out after " + tmo + "ms");
    bleFallback();
  });
  Shelly.call("HTTP.POST", {
    url: "http://" + CONFIG.targetIp + "/rpc",
    body: JSON.stringify({ id: 1, src: "shellyctl-link", method: CONFIG.method, params: CONFIG.params }),
    timeout: CONFIG.httpTimeoutSec
  }, function (res, err_code, err_msg) {
    if (done || token !== attempt) return;
    done = true;
    let rtt = Date.now() - start;
    if (err_code === 0 && res && res.code === 200) {
      recordRtt(rtt);
      print("shellyctl[" + CONFIG.linkId + "]: LAN ok in " + rtt + "ms (ewma " + ((ewmaMs + 0.5) | 0) + "ms)");
    } else {
      print("shellyctl[" + CONFIG.linkId + "]: LAN failed after " + rtt + "ms: " + (err_msg ? err_msg : "HTTP " + (res ? JSON.stringify(res.code) : "?")));
      bleFallback();
    }
  });
}
{{end}}
Shelly.addEventHandler(function (event) {
  if (!event || !event.info) return;
  if (event.component === CONFIG.srcComponent && event.info.event === CONFIG.srcEvent) {
    onTrigger();
  }
});
`
