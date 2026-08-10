package d2d

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Jensen95/shelly-local-comms/internal/scripts"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// SurveyEntry is one neighbor seen by the BLE proximity survey: the
// strongest advertisement RSSI observed for an address, how many packets
// were seen and the advertised local name (often the Shelly device id).
type SurveyEntry struct {
	RSSI  int    `json:"rssi"`
	Count int    `json:"count"`
	Name  string `json:"name"`
}

// StartSurvey installs and starts the temporary BLE proximity survey
// script on dev and returns its script id. The script scans for
// scripts.SurveyDurationMs; call CollectSurvey after that long. Autostart
// stays off so a crash mid-survey cannot leave a scanner running across
// reboots.
func (d *Deployer) StartSurvey(ctx context.Context, dev shelly.Device) (int, error) {
	code, err := scripts.RenderSurvey()
	if err != nil {
		return 0, err
	}
	id, err := d.install(ctx, dev.Addr, scripts.SurveyScriptName, code, false)
	if err != nil {
		// Don't leak the slot (devices have ~10) or a possibly-started
		// scanner when the install failed partway through.
		if id != 0 {
			_ = d.Undeploy(ctx, dev, id)
		}
		return 0, err
	}
	return id, nil
}

// CollectSurvey reads the accumulated scan results from the survey script
// via Script.Eval and then removes the script (best effort — the script
// has already stopped scanning and does not autostart).
func (d *Deployer) CollectSurvey(ctx context.Context, dev shelly.Device, scriptID int) (map[string]SurveyEntry, error) {
	c := d.callerFor(dev.Addr)
	var res struct {
		Result string `json:"result"`
	}
	evalErr := c.Call(ctx, "Script.Eval",
		map[string]any{"id": scriptID, "code": "getResults()"}, &res)
	_ = d.Undeploy(ctx, dev, scriptID)
	if evalErr != nil {
		return nil, fmt.Errorf("d2d: collect survey from %s: %w", dev.Addr, evalErr)
	}

	var seen map[string]SurveyEntry
	if err := json.Unmarshal([]byte(res.Result), &seen); err != nil {
		return nil, fmt.Errorf("d2d: parse survey results from %s: %w", dev.Addr, err)
	}
	if e, ok := seen["__error"]; ok {
		return nil, errors.New("d2d: survey on " + dev.Addr + ": " + e.Name)
	}
	return seen, nil
}
