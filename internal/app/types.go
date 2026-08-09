// Package app defines the application-level model: persisted
// configuration (devices, device-to-device links, shared settings) and the
// Manager interface both user interfaces (TUI and web) are built against.
package app

import (
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// Link declares one device-to-device trigger: an input event on the
// source device invokes an RPC method on the target device, LAN-first
// with BLE RPC fallback. Links are materialized as an mJS script deployed
// to the source device, so they keep working with this tool offline.
type Link struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// SourceDevice / TargetDevice reference Device.Key().
	SourceDevice string `json:"source_device"`
	TargetDevice string `json:"target_device"`

	// SourceComponent is the component watched on the source, e.g. "input:0".
	SourceComponent string `json:"source_component"`
	// SourceEvent is the event that fires the link, e.g. "toggle",
	// "single_push", "double_push", "long_push".
	SourceEvent string `json:"source_event"`

	// TargetMethod and TargetParams are the RPC call made on the target,
	// e.g. "Switch.Set" with {"id":0,"on":true} or "Switch.Toggle".
	TargetMethod string         `json:"target_method"`
	TargetParams map[string]any `json:"target_params,omitempty"`

	// Fallback tunes the LAN→BLE failover behavior of the generated script.
	Fallback FallbackConfig `json:"fallback"`

	// DeployedScriptID is the id of the script slot on the source device
	// once deployed; 0 means not deployed.
	DeployedScriptID int `json:"deployed_script_id,omitempty"`
	// DeployedAt is when the script was last pushed to the device.
	DeployedAt time.Time `json:"deployed_at,omitzero"`
}

// FallbackConfig controls how the generated on-device script decides to
// fail over from LAN RPC to BLE RPC.
type FallbackConfig struct {
	// BLEEnabled enables the BLE RPC fallback tier. Requires the target's
	// BLE MAC to be known and firmware exposing BLE RPC to scripts.
	BLEEnabled bool `json:"ble_enabled"`
	// BaseTimeoutMs is the floor for the LAN RPC timeout.
	BaseTimeoutMs int `json:"base_timeout_ms"`
	// MaxTimeoutMs caps the adaptive LAN timeout so the fallback never
	// feels sluggish even on a congested network.
	MaxTimeoutMs int `json:"max_timeout_ms"`
	// LatencyFactor multiplies the on-device EWMA round-trip latency to
	// derive the current timeout (timeout = clamp(ewma*factor, base, max)).
	LatencyFactor float64 `json:"latency_factor"`
}

// DefaultFallback returns sane failover tuning: 400ms floor, 1.5s cap,
// timeout tracking at 4x observed round-trip latency.
func DefaultFallback() FallbackConfig {
	return FallbackConfig{BLEEnabled: true, BaseTimeoutMs: 400, MaxTimeoutMs: 1500, LatencyFactor: 4}
}

// MQTTSettings is the broker configuration applied to devices in bulk.
type MQTTSettings struct {
	Enable       bool   `json:"enable"`
	Server       string `json:"server"` // host:port
	User         string `json:"user,omitempty"`
	Password     string `json:"pass,omitempty"`
	TopicPrefix  string `json:"topic_prefix,omitempty"`
	RPCNotifs    bool   `json:"rpc_ntf"`
	StatusNotifs bool   `json:"status_ntf"`
}

// BLESettings controls the Bluetooth stack on devices: enabling BLE, the
// RPC channel over BLE, and observer mode (relaying BLU sensor
// advertisements onto the LAN/MQTT).
type BLESettings struct {
	Enable   bool `json:"enable"`
	RPC      bool `json:"rpc"`
	Observer bool `json:"observer"`
}

// LatencyStats is the manager's health view of one device on the LAN.
type LatencyStats struct {
	Device      string        `json:"device"`
	EWMA        time.Duration `json:"ewma"`
	Last        time.Duration `json:"last"`
	Samples     int           `json:"samples"`
	Failures    int           `json:"failures"`
	LastError   string        `json:"last_error,omitempty"`
	LastProbeAt time.Time     `json:"last_probe_at,omitzero"`
	// Degraded is set when latency or failures cross the thresholds that
	// should push deployed links toward their BLE fallback tuning.
	Degraded bool `json:"degraded"`
}

// Config is the persisted state of the tool (JSON on disk).
type Config struct {
	Devices []shelly.Device `json:"devices"`
	Links   []Link          `json:"links"`
	// Passwords maps Device.Key() to the device password. Local file only.
	Passwords map[string]string `json:"passwords,omitempty"`
}
