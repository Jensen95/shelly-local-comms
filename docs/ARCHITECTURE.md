# Architecture

`shellyctl` is a single Go binary for managing Shelly Gen2+ devices
entirely over the local network — no Shelly Cloud, no Home Assistant in
the loop. It ships two frontends (a Bubble Tea TUI and an embedded web
UI) over one shared core.

## Goals

1. **Device-to-device communication without hassle.** Declare "input on
   device A triggers an action on device B" and the tool generates and
   deploys an mJS script to device A. The script runs on the device, so
   links keep working when this tool — and the rest of the network
   infrastructure — is offline.
2. **Two-tier trigger path: LAN first, BLE fallback.** The generated
   script calls the target over local HTTP RPC. When that fails or the
   adaptive timeout fires, it falls back to BLE RPC to the bonded target.
3. **Latency-aware.** Both the manager (for the UI health view) and the
   generated scripts (on-device EWMA of round-trip time) track latency.
   On a congested network the LAN timeout tightens toward the configured
   floor/cap so failover to BLE stays fast instead of hanging on a slow
   WiFi round-trip.
4. **Shared settings in bulk.** MQTT broker config, Bluetooth
   enable/RPC/observer mode, and WiFi range-extender setup applied across
   many devices at once.
5. **Edge devices.** Devices at the fringe of WiFi coverage are bridged
   by enabling the Shelly *range extender* AP on a well-placed device and
   pointing the edge device's WiFi at it.

## Package layout

| Package | Responsibility |
|---|---|
| `internal/shelly` | Gen2+ RPC client (HTTP + WebSocket), digest auth, mDNS discovery (`_shelly._tcp`), shared data model |
| `internal/transport` | Latency prober: periodic health probes, EWMA stats, degradation detection feeding timeout tuning |
| `internal/scripts` | mJS templates for on-device link scripts (LAN→BLE fallback with adaptive timeout) |
| `internal/d2d` | Link lifecycle: render script from a `Link`, deploy via `Script.*` RPCs, verify, undeploy |
| `internal/settings` | Bulk settings: `MQTT.SetConfig`, `BLE.SetConfig` (incl. observer), `WiFi.SetConfig` range extender + edge join, `WiFi.GetStatus` RSSI reads |
| `internal/mqttdisc` | MQTT announce-discovery: subscribes `+/announce`, `shellies/announce`, `+/online`; broadcasts `announce` to `shellies/command`; parses announces (with IP) into devices |
| `internal/app` | Persisted config (JSON store) and the `Manager` facade both UIs consume |
| `internal/tui` | Bubble Tea terminal UI |
| `internal/web` | HTTP API + embedded static web UI (`go:embed`, no Node toolchain) |
| `cmd/shellyctl` | CLI entry: `tui`, `serve`, `discover`, `version` |

Dependency rule: UIs depend only on `internal/app.Manager`. The concrete
manager wires transport/d2d/settings together.

## Trigger strategies

Each link picks one of two on-device strategies:

- **fallback** (default): LAN RPC first with a latency-adaptive timeout;
  BLE RPC only when LAN fails or times out. Safe for any target method,
  including non-idempotent ones like `Switch.Toggle` — exactly one path
  delivers.
- **race**: LAN and BLE fire simultaneously; the faster path wins and the
  slower duplicate is harmless. Lowest worst-case latency (no timeout to
  wait out), but both paths may deliver, so the target method must be
  IDEMPOTENT (e.g. `Switch.Set` with explicit params). Toggle-style
  methods are rejected at save/render time — delivered twice they cancel
  themselves out.

## The generated link script

For each link the source device gets one script that:

1. Subscribes to the configured input event (`Shelly.addEventHandler`).
2. On trigger, issues `HTTP.GET` to `http://<target>/rpc/<method>?...`
   with a timeout derived from an EWMA of previous round-trips:
   `timeout = clamp(ewma * latency_factor, base_timeout, max_timeout)`.
3. On success, updates the EWMA from the measured round-trip.
4. On failure/timeout, calls the target over BLE RPC when enabled and the
   firmware exposes it (feature-detected at script start, so the script
   degrades to LAN-only on older firmware instead of crashing).

BLE notes: BLE RPC availability depends on device generation and
firmware; the deployer verifies before enabling the fallback tier and
records the capability per device. BLE connection slots are limited, so
BLE stays the fallback tier, never the primary.

## Persistence

A single JSON file (`~/.config/shellyctl/config.json`): device registry,
link definitions (including deployed script ids), and optional per-device
passwords. Written atomically.

## Quality gates

- GitHub Actions CI: `go build`, `go vet`, `go test -race`, `golangci-lint`.
- Dependabot: `gomod` + `github-actions`, weekly.
- Unit tests use `httptest` fakes of the Shelly RPC endpoint; script
  generation is covered by golden tests.
