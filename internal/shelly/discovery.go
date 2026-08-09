package shelly

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// mDNS parameters for Shelly Gen2+ devices.
const (
	mdnsService = "_shelly._tcp"
	mdnsDomain  = "local."
)

// enrichment tuning for Discover.
const (
	enrichConcurrency   = 8
	enrichProbeTimeout  = 3 * time.Second
	discoverChannelSize = 16
)

// Discover browses the LAN for Shelly Gen2+ devices via mDNS
// (_shelly._tcp in local.) for the given timeout, then best-effort
// contacts each device to fill in its DeviceInfo. Devices that do not
// answer the info call are still returned with whatever the mDNS entry
// carried. The result is deduplicated by Device.Key().
func Discover(ctx context.Context, timeout time.Duration) ([]Device, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("create mdns resolver: %w", err)
	}

	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	entries := make(chan *zeroconf.ServiceEntry, discoverChannelSize)
	if err := resolver.Browse(browseCtx, mdnsService, mdnsDomain, entries); err != nil {
		return nil, fmt.Errorf("mdns browse: %w", err)
	}

	// The zeroconf mainloop closes the channel when browseCtx is done.
	var devices []Device
	for entry := range entries {
		if d, ok := deviceFromEntry(entry); ok {
			devices = append(devices, d)
		}
	}
	devices = dedupDevices(devices)

	enrichDevices(ctx, devices)

	// Enrichment can reveal that two addresses are the same device
	// (Info.ID becomes the key), so deduplicate again.
	return dedupDevices(devices), nil
}

// deviceFromEntry converts one mDNS service entry into a Device without
// contacting it. The first IPv4 address is preferred; the hostname is the
// fallback. It reports false when the entry carries no usable address.
func deviceFromEntry(entry *zeroconf.ServiceEntry) (Device, bool) {
	if entry == nil {
		return Device{}, false
	}

	var addr string
	for _, ip := range entry.AddrIPv4 {
		if v4 := ip.To4(); v4 != nil {
			addr = v4.String()
			break
		}
	}
	if addr == "" {
		addr = strings.TrimSuffix(strings.TrimSpace(entry.HostName), ".")
	}
	if addr == "" {
		return Device{}, false
	}

	d := Device{Addr: addr, Source: "mdns"}

	// The instance name of a Gen2+ device is its device id
	// (e.g. "shellyplus1pm-a8032abcdef0").
	d.Info.ID = strings.TrimSpace(entry.Instance)

	// Gen2+ devices publish TXT records like gen=2, app=Plus1PM, ver=1.0.0.
	for _, txt := range entry.Text {
		k, v, ok := strings.Cut(txt, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "gen":
			if gen, err := strconv.Atoi(v); err == nil {
				d.Info.Gen = gen
			}
		case "app":
			d.Info.App = v
		case "ver":
			d.Info.Version = v
		case "fw_id":
			d.Info.FirmwareID = v
		case "id":
			d.Info.ID = v
		}
	}
	return d, true
}

// dedupDevices removes devices sharing a Device.Key(), keeping the first
// occurrence. Order is preserved.
func dedupDevices(devs []Device) []Device {
	seen := make(map[string]bool, len(devs))
	out := devs[:0:0]
	for _, d := range devs {
		key := d.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

// enrichDevices best-effort fills Info by calling Shelly.GetDeviceInfo on
// each device, at most enrichConcurrency devices at a time. Failures are
// ignored: the device keeps its mDNS-derived Info.
func enrichDevices(ctx context.Context, devs []Device) {
	sem := make(chan struct{}, enrichConcurrency)
	var wg sync.WaitGroup
	for i := range devs {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *Device) {
			defer wg.Done()
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, enrichProbeTimeout)
			defer cancel()
			if info, err := NewClient(d.Addr).GetDeviceInfo(probeCtx); err == nil {
				d.Info = info
			}
		}(&devs[i])
	}
	wg.Wait()
}
