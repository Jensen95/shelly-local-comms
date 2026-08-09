// Command shellyctl manages Shelly Gen2+ devices on the local network:
// discovery, device-to-device links with LAN→BLE fallback, shared
// settings (MQTT, Bluetooth, WiFi range extender), a terminal UI and an
// embedded web UI.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shellyctl:", err)
		os.Exit(1)
	}
}

func usage() string {
	return `usage: shellyctl <command>

commands:
  tui       start the interactive terminal UI
  serve     start the web UI (default :8790)
  discover  scan the LAN for Shelly devices and print them
  version   print version
`
}
