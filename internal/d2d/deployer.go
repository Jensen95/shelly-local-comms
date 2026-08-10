// Package d2d manages the lifecycle of device-to-device links: it
// renders the per-link mJS script and pushes it to the source device's
// script slot via the Script.* RPC family (create/stop/upload/enable/
// start/verify), and removes it again on undeploy.
package d2d

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/scripts"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// chunkSize is the maximum number of bytes uploaded per Script.PutCode
// call. Shelly devices reject overly large RPC frames, so code is
// streamed in append mode.
const chunkSize = 1024

// Deployer pushes rendered link scripts to source devices.
type Deployer struct {
	callerFor func(addr string) shelly.Caller
}

// NewDeployer returns a Deployer that obtains an RPC caller for a device
// address via callerFor (the wiring supplies authenticated clients;
// tests supply fakes).
func NewDeployer(callerFor func(addr string) shelly.Caller) *Deployer {
	return &Deployer{callerFor: callerFor}
}

// ScriptName is the script slot name used on the source device for a link.
func ScriptName(linkID string) string { return "shellyctl-link-" + linkID }

// Render produces the mJS script the link l would deploy against target.
//
// When the link requests BLE fallback but the target's BLE MAC is
// unknown, the BLE tier is silently downgraded (disabled) rather than
// rendering a script that could never complete a BLE call.
func (d *Deployer) Render(l app.Link, target shelly.Device) (string, error) {
	p := scripts.Params{
		LinkID:          l.ID,
		Name:            l.Name,
		SourceComponent: l.SourceComponent,
		SourceEvent:     l.SourceEvent,
		TargetAddr:      target.Addr,
		TargetBLEMAC:    target.BLEMAC,
		TargetMethod:    l.TargetMethod,
		TargetParams:    l.TargetParams,
		Fallback:        l.Fallback,
	}
	if p.Fallback.BLEEnabled && target.BLEMAC == "" {
		p.Fallback.BLEEnabled = false
	}
	return scripts.Render(p)
}

// scriptListResult mirrors the Script.List RPC result.
type scriptListResult struct {
	Scripts []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"scripts"`
}

// Deploy renders the script for link l and installs it on the source
// device: it reuses the existing slot named shellyctl-link-<LinkID> or
// creates one, stops it, uploads the code in chunks, enables autostart,
// starts it and verifies it is running. It returns the script id.
//
// Deploy refuses targets with authentication enabled: the generated
// script cannot perform digest auth, so the target would reject every
// LAN call and the link would only ever work over BLE (if at all).
func (d *Deployer) Deploy(ctx context.Context, source shelly.Device, l app.Link, target shelly.Device) (int, error) {
	if target.Info.AuthEnabled {
		return 0, fmt.Errorf("d2d: target %s has authentication enabled; the generated on-device script cannot perform digest auth, so the target would reject the link's LAN calls — disable auth on the target to use device-to-device links", target.Addr)
	}
	code, err := d.Render(l, target)
	if err != nil {
		return 0, err
	}
	return d.install(ctx, source.Addr, ScriptName(l.ID), code, true)
}

// install uploads code into the script slot named name on the device at
// addr (reusing or creating the slot), sets its boot-autostart flag,
// starts it and verifies it is running. It returns the script id.
func (d *Deployer) install(ctx context.Context, addr, name, code string, autostart bool) (int, error) {
	c := d.callerFor(addr)

	var list scriptListResult
	if err := c.Call(ctx, "Script.List", nil, &list); err != nil {
		return 0, fmt.Errorf("d2d: list scripts on %s: %w", addr, err)
	}
	id := 0
	for _, s := range list.Scripts {
		if s.Name == name {
			id = s.ID
			break
		}
	}
	if id == 0 {
		var created struct {
			ID int `json:"id"`
		}
		if err := c.Call(ctx, "Script.Create", map[string]any{"name": name}, &created); err != nil {
			return 0, fmt.Errorf("d2d: create script %q on %s: %w", name, addr, err)
		}
		id = created.ID
	}

	// Stop before uploading. A freshly created or already-stopped script
	// answers with a device-side RPC error ("not running"); that is fine.
	// Transport-level failures are not.
	if err := c.Call(ctx, "Script.Stop", map[string]any{"id": id}, nil); err != nil {
		var rpcErr *shelly.RPCError
		if !errors.As(err, &rpcErr) {
			return 0, fmt.Errorf("d2d: stop script %d on %s: %w", id, addr, err)
		}
	}

	for i := 0; i < len(code); {
		end := chunkEnd(code, i)
		params := map[string]any{"id": id, "code": code[i:end], "append": i > 0}
		if err := c.Call(ctx, "Script.PutCode", params, nil); err != nil {
			return 0, fmt.Errorf("d2d: upload script %d chunk at %d on %s: %w", id, i, addr, err)
		}
		i = end
	}

	if err := c.Call(ctx, "Script.SetConfig", map[string]any{"id": id, "config": map[string]any{"enable": autostart}}, nil); err != nil {
		return 0, fmt.Errorf("d2d: configure script %d on %s: %w", id, addr, err)
	}
	if err := c.Call(ctx, "Script.Start", map[string]any{"id": id}, nil); err != nil {
		return 0, fmt.Errorf("d2d: start script %d on %s: %w", id, addr, err)
	}

	var status struct {
		Running bool `json:"running"`
	}
	if err := c.Call(ctx, "Script.GetStatus", map[string]any{"id": id}, &status); err != nil {
		return 0, fmt.Errorf("d2d: get status of script %d on %s: %w", id, addr, err)
	}
	if !status.Running {
		return 0, fmt.Errorf("d2d: script %d on %s did not start (running=false); check the device's script console for errors", id, addr)
	}
	return id, nil
}

// chunkEnd returns the end index of the chunk starting at i, at most
// chunkSize bytes, never splitting a UTF-8 sequence (Script.PutCode
// carries the chunk as a JSON string, which must be valid UTF-8).
func chunkEnd(code string, i int) int {
	end := i + chunkSize
	if end >= len(code) {
		return len(code)
	}
	for end > i && !utf8.RuneStart(code[end]) {
		end--
	}
	return end
}

// Undeploy stops (best effort) and deletes the script slot scriptID on
// the source device.
func (d *Deployer) Undeploy(ctx context.Context, source shelly.Device, scriptID int) error {
	c := d.callerFor(source.Addr)
	// Tolerate stop errors entirely: the script may not be running, may
	// already be gone, or the slot may be in a broken state — Delete is
	// what matters.
	_ = c.Call(ctx, "Script.Stop", map[string]any{"id": scriptID}, nil)
	if err := c.Call(ctx, "Script.Delete", map[string]any{"id": scriptID}, nil); err != nil {
		return fmt.Errorf("d2d: delete script %d on %s: %w", scriptID, source.Addr, err)
	}
	return nil
}
