# shelly-local-comms

`shellyctl` — local-first management for Shelly Gen2+ devices. No Shelly
Cloud, no Home Assistant required.

- **Device-to-device links**: an input on one Shelly triggers an action on
  another via an auto-generated on-device script — LAN RPC first, **BLE RPC
  fallback** when WiFi is down, with latency-adaptive timeouts so failover
  stays fast on a congested network. For idempotent actions there is also a
  **race strategy**: LAN and BLE fire together and the faster path wins.
- **Bulk shared settings**: MQTT broker config, Bluetooth enable / BLE RPC /
  BLU observer mode across many devices at once.
- **Edge devices**: bridge devices at the fringe of WiFi coverage through a
  well-placed Shelly's built-in range-extender access point.
- **Auto-discovery**: devices are found via mDNS — on demand and with a
  background sweep (every 5 minutes by default, `-discover-interval` to
  tune or disable) so new Shellys appear on their own. Once a broker has
  been applied, **MQTT announce-discovery** runs too: shellyctl asks all
  devices on the broker to announce themselves (their announce includes
  their IP), which also finds devices on subnets mDNS cannot cross. Every
  manual Discover action re-sweeps both channels.
- **Extender suggestions**: finds devices with weak WiFi, then has each
  one run a short on-device **BLE proximity scan** to identify its
  physically closest neighbor with a healthy uplink — that neighbor is
  suggested as the range-extender. Falls back to strongest-router-signal
  (clearly labeled) when the BLE survey can't run.
- **Two frontends**: a terminal UI (`shellyctl tui`) and an embedded web UI
  (`shellyctl serve`), both over the same core.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design.

## Install

```sh
go install github.com/Jensen95/shelly-local-comms/cmd/shellyctl@latest
```

## Quick start

```sh
shellyctl discover      # find devices on the LAN (mDNS)
shellyctl tui           # interactive terminal UI
shellyctl serve         # web UI on http://localhost:8790
```

## Development

```sh
go build ./...
go test -race ./...
```

CI runs build, vet, race tests and golangci-lint; Dependabot keeps Go
modules and GitHub Actions current.
