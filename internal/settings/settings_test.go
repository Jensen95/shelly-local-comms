package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

// fakeCaller records the last RPC invocation and replays a canned result
// or error. params is the marshaled JSON of what was passed, so tests
// can assert the exact payload shape (encoding/json sorts map keys, so
// string comparison is deterministic).
type fakeCaller struct {
	calls  int
	method string
	params string
	result any
	err    error
}

func (f *fakeCaller) Call(_ context.Context, method string, params any, out any) error {
	f.calls++
	f.method = method
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	f.params = string(b)
	if f.err != nil {
		return f.err
	}
	if out != nil && f.result != nil {
		b, err := json.Marshal(f.result)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	return nil
}

func restartTrue() map[string]any  { return map[string]any{"restart_required": true} }
func restartFalse() map[string]any { return map[string]any{"restart_required": false} }

func TestApplyMQTTFullConfig(t *testing.T) {
	f := &fakeCaller{result: restartTrue()}
	restart, err := ApplyMQTT(context.Background(), f, app.MQTTSettings{
		Enable:       true,
		Server:       "broker.local:1883",
		User:         "mqtt-user",
		Password:     "hunter2",
		TopicPrefix:  "shellies/kitchen",
		RPCNotifs:    true,
		StatusNotifs: true,
	})
	if err != nil {
		t.Fatalf("ApplyMQTT: %v", err)
	}
	if !restart {
		t.Error("restart_required = false, want true")
	}
	if f.method != "MQTT.SetConfig" {
		t.Errorf("method = %q, want MQTT.SetConfig", f.method)
	}
	want := `{"config":{"enable":true,"pass":"hunter2","rpc_ntf":true,"server":"broker.local:1883","status_ntf":true,"topic_prefix":"shellies/kitchen","user":"mqtt-user"}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestApplyMQTTOmitsEmptyOptionalKeys(t *testing.T) {
	f := &fakeCaller{result: restartTrue()}
	_, err := ApplyMQTT(context.Background(), f, app.MQTTSettings{
		Enable:       true,
		Server:       "broker.local:1883",
		RPCNotifs:    false,
		StatusNotifs: true,
	})
	if err != nil {
		t.Fatalf("ApplyMQTT: %v", err)
	}
	want := `{"config":{"enable":true,"rpc_ntf":false,"server":"broker.local:1883","status_ntf":true}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
	for _, key := range []string{"user", "pass", "topic_prefix"} {
		if strings.Contains(f.params, `"`+key+`"`) {
			t.Errorf("params contain %q key, want it omitted: %s", key, f.params)
		}
	}
}

func TestApplyMQTTRestartRequiredPlumbing(t *testing.T) {
	f := &fakeCaller{result: restartFalse()}
	restart, err := ApplyMQTT(context.Background(), f, app.MQTTSettings{Server: "b:1883"})
	if err != nil {
		t.Fatalf("ApplyMQTT: %v", err)
	}
	if restart {
		t.Error("restart_required = true, want false (device said false)")
	}
}

func TestApplyMQTTError(t *testing.T) {
	boom := errors.New("device unreachable")
	f := &fakeCaller{err: boom}
	restart, err := ApplyMQTT(context.Background(), f, app.MQTTSettings{Server: "b:1883"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if restart {
		t.Error("restart_required = true on error, want false")
	}
}

func TestApplyBLEPayload(t *testing.T) {
	f := &fakeCaller{result: restartTrue()}
	restart, err := ApplyBLE(context.Background(), f, app.BLESettings{Enable: true, RPC: true, Observer: true})
	if err != nil {
		t.Fatalf("ApplyBLE: %v", err)
	}
	if !restart {
		t.Error("restart_required = false, want true")
	}
	if f.method != "BLE.SetConfig" {
		t.Errorf("method = %q, want BLE.SetConfig", f.method)
	}
	want := `{"config":{"enable":true,"observer":{"enable":true},"rpc":{"enable":true}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestApplyBLEAllDisabled(t *testing.T) {
	f := &fakeCaller{result: restartFalse()}
	if _, err := ApplyBLE(context.Background(), f, app.BLESettings{}); err != nil {
		t.Fatalf("ApplyBLE: %v", err)
	}
	want := `{"config":{"enable":false,"observer":{"enable":false},"rpc":{"enable":false}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestApplyBLEValidation(t *testing.T) {
	tests := []struct {
		name    string
		s       app.BLESettings
		wantSub string
	}{
		{"observer without enable", app.BLESettings{Observer: true}, "observer"},
		{"rpc without enable", app.BLESettings{RPC: true}, "RPC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeCaller{}
			_, err := ApplyBLE(context.Background(), f, tt.s)
			if err == nil {
				t.Fatal("ApplyBLE: expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not mention %q", err, tt.wantSub)
			}
			if f.calls != 0 {
				t.Errorf("device was called %d times, want 0 (validation must precede RPC)", f.calls)
			}
		})
	}
}

func TestReboot(t *testing.T) {
	f := &fakeCaller{}
	if err := Reboot(context.Background(), f); err != nil {
		t.Fatalf("Reboot: %v", err)
	}
	if f.method != "Shelly.Reboot" {
		t.Errorf("method = %q, want Shelly.Reboot", f.method)
	}
	if f.params != "null" {
		t.Errorf("params = %s, want null (no params)", f.params)
	}
}

func TestRebootError(t *testing.T) {
	boom := errors.New("nope")
	f := &fakeCaller{err: boom}
	if err := Reboot(context.Background(), f); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestSetRangeExtenderEnable(t *testing.T) {
	f := &fakeCaller{result: restartTrue()}
	restart, err := SetRangeExtender(context.Background(), f, true)
	if err != nil {
		t.Fatalf("SetRangeExtender: %v", err)
	}
	if !restart {
		t.Error("restart_required = false, want true")
	}
	if f.method != "WiFi.SetConfig" {
		t.Errorf("method = %q, want WiFi.SetConfig", f.method)
	}
	// Enabling the extender must also enable the AP in the same call.
	want := `{"config":{"ap":{"enable":true,"range_extender":{"enable":true}}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestSetRangeExtenderDisableLeavesAPAlone(t *testing.T) {
	f := &fakeCaller{result: restartFalse()}
	if _, err := SetRangeExtender(context.Background(), f, false); err != nil {
		t.Fatalf("SetRangeExtender: %v", err)
	}
	want := `{"config":{"ap":{"range_extender":{"enable":false}}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestGetAPInfo(t *testing.T) {
	f := &fakeCaller{result: map[string]any{
		"ap": map[string]any{
			"ssid":           "shelly-ext",
			"pass":           "sekrit",
			"enable":         true,
			"is_open":        false,
			"range_extender": map[string]any{"enable": true},
		},
	}}
	info, err := GetAPInfo(context.Background(), f)
	if err != nil {
		t.Fatalf("GetAPInfo: %v", err)
	}
	if f.method != "WiFi.GetConfig" {
		t.Errorf("method = %q, want WiFi.GetConfig", f.method)
	}
	want := APInfo{SSID: "shelly-ext", Password: "sekrit", Enabled: true, RangeExtender: true, OpenAuth: false}
	if info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
}

func TestGetAPInfoPassAbsentAndOpen(t *testing.T) {
	f := &fakeCaller{result: map[string]any{
		"ap": map[string]any{
			"ssid":           "shelly-open",
			"enable":         true,
			"is_open":        true,
			"range_extender": map[string]any{"enable": false},
		},
	}}
	info, err := GetAPInfo(context.Background(), f)
	if err != nil {
		t.Fatalf("GetAPInfo: %v", err)
	}
	want := APInfo{SSID: "shelly-open", Password: "", Enabled: true, RangeExtender: false, OpenAuth: true}
	if info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
}

func TestGetAPInfoError(t *testing.T) {
	boom := errors.New("timeout")
	f := &fakeCaller{err: boom}
	if _, err := GetAPInfo(context.Background(), f); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestJoinExtenderWithPassword(t *testing.T) {
	f := &fakeCaller{result: restartTrue()}
	restart, err := JoinExtender(context.Background(), f, APInfo{
		SSID: "shelly-ext", Password: "sekrit", Enabled: true,
	})
	if err != nil {
		t.Fatalf("JoinExtender: %v", err)
	}
	if !restart {
		t.Error("restart_required = false, want true")
	}
	if f.method != "WiFi.SetConfig" {
		t.Errorf("method = %q, want WiFi.SetConfig", f.method)
	}
	want := `{"config":{"sta1":{"enable":true,"pass":"sekrit","ssid":"shelly-ext"}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
}

func TestJoinExtenderOpenAPOmitsPass(t *testing.T) {
	f := &fakeCaller{result: restartFalse()}
	if _, err := JoinExtender(context.Background(), f, APInfo{
		SSID: "shelly-open", Enabled: true, OpenAuth: true,
	}); err != nil {
		t.Fatalf("JoinExtender: %v", err)
	}
	want := `{"config":{"sta1":{"enable":true,"ssid":"shelly-open"}}}`
	if f.params != want {
		t.Errorf("params = %s\nwant     %s", f.params, want)
	}
	if strings.Contains(f.params, `"pass"`) {
		t.Errorf("params contain pass key for open AP: %s", f.params)
	}
}

func TestJoinExtenderValidation(t *testing.T) {
	tests := []struct {
		name    string
		ap      APInfo
		wantSub string
	}{
		{"empty ssid", APInfo{Enabled: true}, "SSID"},
		{"ap not enabled", APInfo{SSID: "shelly-ext"}, "not enabled"},
		// Shelly never returns the AP password over RPC; joining a
		// protected AP without one would silently configure an empty
		// password the edge device can never associate with.
		{"protected ap without password", APInfo{SSID: "shelly-ext", Enabled: true}, "password-protected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeCaller{}
			_, err := JoinExtender(context.Background(), f, tt.ap)
			if err == nil {
				t.Fatal("JoinExtender: expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not mention %q", err, tt.wantSub)
			}
			if f.calls != 0 {
				t.Errorf("device was called %d times, want 0 (validation must precede RPC)", f.calls)
			}
		})
	}
}
