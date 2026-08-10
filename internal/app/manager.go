package app

import (
	"context"

	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// Manager is the single facade both user interfaces (TUI and web) talk
// to. It is implemented by the concrete application wiring in this
// package; UIs must not import transport/d2d/settings packages directly.
type Manager interface {
	// --- Device registry ---

	// Devices returns the known devices from configuration.
	Devices() []shelly.Device
	// Discover scans the LAN via mDNS for timeoutSeconds and merges the
	// results into the registry, returning newly found devices.
	Discover(ctx context.Context, timeoutSeconds int) ([]shelly.Device, error)
	// AddDevice registers a device manually by address (with optional
	// password), contacting it to fill in device info.
	AddDevice(ctx context.Context, addr, password string) (shelly.Device, error)
	// RemoveDevice drops a device from the registry.
	RemoveDevice(key string) error

	// --- Device-to-device links ---

	Links() []Link
	// SaveLink creates or updates a link definition (matched by ID; empty
	// ID means create).
	SaveLink(l Link) (Link, error)
	DeleteLink(ctx context.Context, id string) error
	// DeployLink generates the mJS script for the link and pushes it to
	// the source device (create/update script slot, start it).
	DeployLink(ctx context.Context, id string) error
	// RenderLinkScript returns the mJS the link would deploy, for preview.
	RenderLinkScript(id string) (string, error)

	// --- Shared settings ---

	// ApplyMQTT pushes broker settings to the given devices (all when
	// keys is empty) and reboots them as required.
	ApplyMQTT(ctx context.Context, s MQTTSettings, keys []string) error
	// ApplyBLE pushes Bluetooth settings (enable/RPC/observer) to the
	// given devices (all when keys is empty).
	ApplyBLE(ctx context.Context, s BLESettings, keys []string) error
	// EnableRangeExtender turns a well-placed device into a WiFi access
	// point bridging edge devices onto the network; JoinExtender points an
	// edge device at that AP.
	EnableRangeExtender(ctx context.Context, key string, enable bool) error
	JoinExtender(ctx context.Context, edgeKey, extenderKey string) error
	// SuggestExtenders finds weak-WiFi devices and suggests an extender
	// for each. It deploys a temporary BLE scan script to every weak
	// device (side effect; roughly 15 seconds wall time, surveys run in
	// parallel) and pairs it with the neighbor it hears loudest that has
	// a healthy uplink; when the survey cannot run it falls back to the
	// strongest-router-signal device, labeled via Method.
	SuggestExtenders(ctx context.Context) ([]ExtenderSuggestion, error)

	// --- Health / latency ---

	// LatencyStats returns the current per-device LAN health snapshot.
	LatencyStats() []LatencyStats
	// ProbeNow forces an immediate probe round of all devices.
	ProbeNow(ctx context.Context) []LatencyStats
}
