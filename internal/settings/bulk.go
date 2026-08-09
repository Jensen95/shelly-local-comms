package settings

import (
	"context"
	"sort"
	"sync"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// bulkConcurrency bounds how many devices are configured at once during
// bulk application.
const bulkConcurrency = 8

// Result is the outcome of applying a setting to one device in a bulk
// operation.
type Result struct {
	Device          string
	RestartRequired bool
	Err             error
}

// ApplyMQTTBulk applies broker settings to every device in callers
// concurrently (at most 8 in flight). One device failing never aborts
// the batch; each device gets its own Result. Results are sorted by
// device key.
func ApplyMQTTBulk(ctx context.Context, callers map[string]shelly.Caller, s app.MQTTSettings) []Result {
	return applyBulk(ctx, callers, func(ctx context.Context, c shelly.Caller) (bool, error) {
		return ApplyMQTT(ctx, c, s)
	})
}

// ApplyBLEBulk applies Bluetooth settings to every device in callers
// concurrently (at most 8 in flight). One device failing never aborts
// the batch; each device gets its own Result. Results are sorted by
// device key.
func ApplyBLEBulk(ctx context.Context, callers map[string]shelly.Caller, s app.BLESettings) []Result {
	return applyBulk(ctx, callers, func(ctx context.Context, c shelly.Caller) (bool, error) {
		return ApplyBLE(ctx, c, s)
	})
}

// applyBulk fans apply out over the callers with bounded concurrency and
// collects one Result per device, sorted by device key.
func applyBulk(ctx context.Context, callers map[string]shelly.Caller, apply func(context.Context, shelly.Caller) (bool, error)) []Result {
	results := make([]Result, 0, len(callers))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, bulkConcurrency)
	)
	for device, c := range callers {
		wg.Add(1)
		go func(device string, c shelly.Caller) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			restart, err := apply(ctx, c)
			mu.Lock()
			results = append(results, Result{Device: device, RestartRequired: restart, Err: err})
			mu.Unlock()
		}(device, c)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Device < results[j].Device })
	return results
}
