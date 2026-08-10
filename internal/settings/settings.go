// Package settings implements thin wrappers over the Shelly Gen2+
// configuration RPCs (MQTT.SetConfig, BLE.SetConfig, WiFi.SetConfig,
// Shelly.Reboot) plus bounded-concurrency bulk application across many
// devices. Every function takes a shelly.Caller so tests can substitute
// fakes and the manager can reuse authenticated clients.
package settings

import (
	"context"
	"fmt"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// setConfigResult mirrors the common response shape of the Xxx.SetConfig
// RPC family: {"restart_required": bool}.
type setConfigResult struct {
	RestartRequired bool `json:"restart_required"`
}

// setConfig invokes an Xxx.SetConfig method with the Shelly convention
// payload {"config": {...}} and returns the device's restart_required.
func setConfig(ctx context.Context, c shelly.Caller, method string, config map[string]any) (bool, error) {
	var res setConfigResult
	if err := c.Call(ctx, method, map[string]any{"config": config}, &res); err != nil {
		return false, err
	}
	return res.RestartRequired, nil
}

// ApplyMQTT pushes broker settings to one device via MQTT.SetConfig.
// The user, pass, and topic_prefix keys are omitted entirely when empty
// so existing device values are left untouched. MQTT config changes
// always require a restart on real devices; the device's own
// restart_required is returned.
func ApplyMQTT(ctx context.Context, c shelly.Caller, s app.MQTTSettings) (restartRequired bool, err error) {
	config := map[string]any{
		"enable":     s.Enable,
		"server":     s.Server,
		"rpc_ntf":    s.RPCNotifs,
		"status_ntf": s.StatusNotifs,
	}
	if s.User != "" {
		config["user"] = s.User
	}
	if s.Password != "" {
		config["pass"] = s.Password
	}
	if s.TopicPrefix != "" {
		config["topic_prefix"] = s.TopicPrefix
	}
	return setConfig(ctx, c, "MQTT.SetConfig", config)
}

// ApplyBLE pushes Bluetooth settings to one device via BLE.SetConfig.
// RPC-over-BLE and observer mode both require the BLE stack itself to be
// enabled; requesting either with Enable false is rejected before any
// RPC is made.
func ApplyBLE(ctx context.Context, c shelly.Caller, s app.BLESettings) (bool, error) {
	if s.RPC && !s.Enable {
		return false, fmt.Errorf("ble settings: RPC over BLE requires BLE to be enabled")
	}
	if s.Observer && !s.Enable {
		return false, fmt.Errorf("ble settings: observer mode requires BLE to be enabled")
	}
	config := map[string]any{
		"enable":   s.Enable,
		"rpc":      map[string]any{"enable": s.RPC},
		"observer": map[string]any{"enable": s.Observer},
	}
	return setConfig(ctx, c, "BLE.SetConfig", config)
}

// Reboot restarts the device via Shelly.Reboot.
func Reboot(ctx context.Context, c shelly.Caller) error {
	return c.Call(ctx, "Shelly.Reboot", nil, nil)
}

// SetRangeExtender enables or disables the WiFi range extender on the
// device's access point via WiFi.SetConfig. Enabling the extender also
// enables the AP itself in the same call, since the extender is useless
// with the AP down; disabling only turns the extender off and leaves the
// AP state alone.
func SetRangeExtender(ctx context.Context, c shelly.Caller, enable bool) (bool, error) {
	ap := map[string]any{
		"range_extender": map[string]any{"enable": enable},
	}
	if enable {
		ap["enable"] = true
	}
	return setConfig(ctx, c, "WiFi.SetConfig", map[string]any{"ap": ap})
}

// APInfo describes a device's WiFi access point as needed to join edge
// devices to it.
type APInfo struct {
	SSID          string
	Password      string
	Enabled       bool
	RangeExtender bool
	// OpenAuth is set when the AP requires no password (result.ap.is_open).
	OpenAuth bool
}

// GetAPInfo reads the device's access point configuration via
// WiFi.GetConfig. The pass field may be absent from the device response;
// Password is empty in that case.
func GetAPInfo(ctx context.Context, c shelly.Caller) (APInfo, error) {
	var res struct {
		AP struct {
			SSID          string  `json:"ssid"`
			Pass          *string `json:"pass"`
			Enable        bool    `json:"enable"`
			IsOpen        bool    `json:"is_open"`
			RangeExtender struct {
				Enable bool `json:"enable"`
			} `json:"range_extender"`
		} `json:"ap"`
	}
	if err := c.Call(ctx, "WiFi.GetConfig", nil, &res); err != nil {
		return APInfo{}, err
	}
	info := APInfo{
		SSID:          res.AP.SSID,
		Enabled:       res.AP.Enable,
		RangeExtender: res.AP.RangeExtender.Enable,
		OpenAuth:      res.AP.IsOpen,
	}
	if res.AP.Pass != nil {
		info.Password = *res.AP.Pass
	}
	return info, nil
}

// JoinExtender points the edge device's fallback WiFi network (sta1) at
// the extender's access point via WiFi.SetConfig. Using sta1 keeps the
// edge device's primary network config intact — the device only falls
// back to the extender AP when the primary is unreachable. The pass key
// is omitted when the AP is open.
func JoinExtender(ctx context.Context, edge shelly.Caller, ap APInfo) (bool, error) {
	if ap.SSID == "" {
		return false, fmt.Errorf("join extender: extender AP has no SSID")
	}
	if !ap.Enabled {
		return false, fmt.Errorf("join extender: extender AP %q is not enabled", ap.SSID)
	}
	// Shelly devices do not return the AP password from WiFi.GetConfig, so
	// a protected AP whose password we don't have would be joined with an
	// empty one: the RPC succeeds but the edge device can never associate.
	if !ap.OpenAuth && ap.Password == "" {
		return false, fmt.Errorf("join extender: AP %q is password-protected but the device does not expose its password over RPC — supply it manually or make the AP open", ap.SSID)
	}
	sta1 := map[string]any{
		"ssid":   ap.SSID,
		"enable": true,
	}
	if !ap.OpenAuth {
		sta1["pass"] = ap.Password
	}
	return setConfig(ctx, edge, "WiFi.SetConfig", map[string]any{"sta1": sta1})
}
