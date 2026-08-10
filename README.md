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
  tune or disable) so new Shellys appear on their own.
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
