// Package shelly implements a client for the Shelly Gen2+ local RPC
// protocol (JSON-RPC over HTTP and WebSocket), device discovery and the
// data model shared by the rest of the application.
package shelly

import (
	"context"
	"encoding/json"
	"fmt"
)

// DeviceInfo mirrors the result of the Shelly.GetDeviceInfo RPC method.
type DeviceInfo struct {
	ID          string `json:"id"`
	MAC         string `json:"mac"`
	Model       string `json:"model"`
	Gen         int    `json:"gen"`
	FirmwareID  string `json:"fw_id"`
	Version     string `json:"ver"`
	App         string `json:"app"`
	AuthEnabled bool   `json:"auth_en"`
	Name        string `json:"name"`
}

// Device is a Shelly device known to the manager, whether discovered via
// mDNS or added manually by IP.
type Device struct {
	// Addr is the host (IP or hostname) the device is reachable at on the LAN.
	Addr string `json:"addr"`
	// Info is the last known device info, populated on first successful contact.
	Info DeviceInfo `json:"info"`
	// BLEMAC is the Bluetooth MAC used for BLE RPC fallback, if known.
	BLEMAC string `json:"ble_mac,omitempty"`
	// Source records how the device entered the registry: "mdns" or "manual".
	Source string `json:"source,omitempty"`
}

// Key returns the stable identifier used to reference the device in
// configuration. Falls back to the address until first contact fills Info.ID.
func (d Device) Key() string {
	if d.Info.ID != "" {
		return d.Info.ID
	}
	return d.Addr
}

// RPCRequest is a single Shelly RPC frame.
type RPCRequest struct {
	ID     int        `json:"id"`
	Src    string     `json:"src,omitempty"`
	Method string     `json:"method"`
	Params any        `json:"params,omitempty"`
	Auth   *AuthFrame `json:"auth,omitempty"`
}

// AuthFrame is the RPC-level digest authentication object used on
// channels that do not carry HTTP headers (e.g. WebSocket).
type AuthFrame struct {
	Realm     string `json:"realm"`
	Username  string `json:"username"`
	Nonce     int64  `json:"nonce"`
	CNonce    int64  `json:"cnonce"`
	Response  string `json:"response"`
	Algorithm string `json:"algorithm"`
}

// RPCResponse is a single Shelly RPC response frame.
type RPCResponse struct {
	ID     int             `json:"id"`
	Src    string          `json:"src"`
	Dst    string          `json:"dst"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// RPCError is the error object of a failed RPC call.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("shelly rpc error %d: %s", e.Code, e.Message)
}

// Caller is the minimal RPC surface the rest of the application depends
// on. *Client implements it; tests substitute fakes.
type Caller interface {
	// Call invokes method with params and unmarshals the result into out
	// when out is non-nil.
	Call(ctx context.Context, method string, params any, out any) error
}
