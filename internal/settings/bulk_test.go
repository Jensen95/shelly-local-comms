package settings

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

func TestApplyMQTTBulkMixedResults(t *testing.T) {
	boom := errors.New("device unreachable")
	callers := map[string]shelly.Caller{
		"charlie": &fakeCaller{result: restartTrue()},
		"alpha":   &fakeCaller{result: restartFalse()},
		"bravo":   &fakeCaller{err: boom},
	}
	results := ApplyMQTTBulk(context.Background(), callers, app.MQTTSettings{Enable: true, Server: "b:1883"})

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	// Sorted by device key.
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		if results[i].Device != want {
			t.Errorf("results[%d].Device = %q, want %q", i, results[i].Device, want)
		}
	}
	if results[0].Err != nil || results[0].RestartRequired {
		t.Errorf("alpha = %+v, want success without restart", results[0])
	}
	if !errors.Is(results[1].Err, boom) {
		t.Errorf("bravo.Err = %v, want %v", results[1].Err, boom)
	}
	if results[2].Err != nil || !results[2].RestartRequired {
		t.Errorf("charlie = %+v, want success with restart", results[2])
	}
	// One failure must not stop the others from being attempted.
	for key, c := range callers {
		if c.(*fakeCaller).calls != 1 {
			t.Errorf("device %s called %d times, want 1", key, c.(*fakeCaller).calls)
		}
		if got := c.(*fakeCaller).method; got != "MQTT.SetConfig" {
			t.Errorf("device %s method = %q, want MQTT.SetConfig", key, got)
		}
	}
}

func TestApplyBLEBulkValidationPerDevice(t *testing.T) {
	callers := map[string]shelly.Caller{
		"a": &fakeCaller{},
		"b": &fakeCaller{},
	}
	// Invalid combination: observer requires BLE enabled. Every device
	// reports the validation error and no RPC is made.
	results := ApplyBLEBulk(context.Background(), callers, app.BLESettings{Observer: true})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Err == nil {
			t.Errorf("device %s: expected validation error, got nil", r.Device)
		}
	}
	for key, c := range callers {
		if c.(*fakeCaller).calls != 0 {
			t.Errorf("device %s was called, want no RPC on validation failure", key)
		}
	}
}

func TestApplyBLEBulkSuccess(t *testing.T) {
	callers := map[string]shelly.Caller{
		"a": &fakeCaller{result: restartTrue()},
		"b": &fakeCaller{result: restartFalse()},
	}
	results := ApplyBLEBulk(context.Background(), callers, app.BLESettings{Enable: true, RPC: true})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Device != "a" || !results[0].RestartRequired || results[0].Err != nil {
		t.Errorf("results[0] = %+v, want a/restart/nil", results[0])
	}
	if results[1].Device != "b" || results[1].RestartRequired || results[1].Err != nil {
		t.Errorf("results[1] = %+v, want b/no-restart/nil", results[1])
	}
}

func TestApplyBulkEmpty(t *testing.T) {
	results := ApplyMQTTBulk(context.Background(), nil, app.MQTTSettings{})
	if len(results) != 0 {
		t.Fatalf("got %d results for nil callers, want 0", len(results))
	}
}

// slowCaller tracks how many Calls run concurrently.
type slowCaller struct {
	inFlight *atomic.Int64
	peak     *atomic.Int64
}

func (s *slowCaller) Call(_ context.Context, _ string, _ any, _ any) error {
	n := s.inFlight.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	s.inFlight.Add(-1)
	return nil
}

func TestApplyBulkBoundedConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int64
	callers := make(map[string]shelly.Caller, 30)
	for i := 0; i < 30; i++ {
		callers[string(rune('a'+i%26))+string(rune('0'+i/26))] = &slowCaller{inFlight: &inFlight, peak: &peak}
	}
	results := ApplyMQTTBulk(context.Background(), callers, app.MQTTSettings{Server: "b:1883"})
	if len(results) != len(callers) {
		t.Fatalf("got %d results, want %d", len(results), len(callers))
	}
	if p := peak.Load(); p > 8 {
		t.Errorf("peak concurrency = %d, want <= 8", p)
	}
	for i := 1; i < len(results); i++ {
		if results[i-1].Device >= results[i].Device {
			t.Errorf("results not sorted: %q before %q", results[i-1].Device, results[i].Device)
		}
	}
}
