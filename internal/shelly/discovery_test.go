package shelly

import (
	"net"
	"reflect"
	"testing"

	"github.com/grandcat/zeroconf"
)

func TestDeviceFromEntryPrefersIPv4(t *testing.T) {
	entry := zeroconf.NewServiceEntry("shellyplus1pm-a8032abcdef0", mdnsService, mdnsDomain)
	entry.HostName = "ShellyPlus1PM-A8032ABCDEF0.local."
	entry.AddrIPv4 = []net.IP{net.ParseIP("192.168.1.42"), net.ParseIP("192.168.1.43")}
	entry.AddrIPv6 = []net.IP{net.ParseIP("fe80::1")}
	entry.Text = []string{"gen=2", "app=Plus1PM", "ver=1.4.4"}

	d, ok := deviceFromEntry(entry)
	if !ok {
		t.Fatal("deviceFromEntry returned ok=false")
	}
	if d.Addr != "192.168.1.42" {
		t.Errorf("Addr = %q, want first IPv4 192.168.1.42", d.Addr)
	}
	if d.Source != "mdns" {
		t.Errorf("Source = %q, want mdns", d.Source)
	}
	if d.Info.ID != "shellyplus1pm-a8032abcdef0" {
		t.Errorf("Info.ID = %q, want instance name", d.Info.ID)
	}
	if d.Info.Gen != 2 {
		t.Errorf("Info.Gen = %d, want 2", d.Info.Gen)
	}
	if d.Info.App != "Plus1PM" {
		t.Errorf("Info.App = %q, want Plus1PM", d.Info.App)
	}
	if d.Info.Version != "1.4.4" {
		t.Errorf("Info.Version = %q, want 1.4.4", d.Info.Version)
	}
}

func TestDeviceFromEntryHostnameFallback(t *testing.T) {
	entry := zeroconf.NewServiceEntry("shellypro4pm-0123456789ab", mdnsService, mdnsDomain)
	entry.HostName = "ShellyPro4PM-0123456789AB.local."

	d, ok := deviceFromEntry(entry)
	if !ok {
		t.Fatal("deviceFromEntry returned ok=false")
	}
	if d.Addr != "ShellyPro4PM-0123456789AB.local" {
		t.Errorf("Addr = %q, want hostname without trailing dot", d.Addr)
	}
}

func TestDeviceFromEntryNoAddress(t *testing.T) {
	entry := zeroconf.NewServiceEntry("shelly1-abc", mdnsService, mdnsDomain)
	if d, ok := deviceFromEntry(entry); ok {
		t.Errorf("deviceFromEntry = (%+v, true), want ok=false for entry without address", d)
	}
	if _, ok := deviceFromEntry(nil); ok {
		t.Error("deviceFromEntry(nil) returned ok=true")
	}
}

func TestDeviceFromEntryTXTParsing(t *testing.T) {
	entry := zeroconf.NewServiceEntry("instance-name", mdnsService, mdnsDomain)
	entry.AddrIPv4 = []net.IP{net.ParseIP("10.0.0.5")}
	entry.Text = []string{
		"id=shellyplusi4-cafe00112233", // explicit id overrides instance name
		"GEN=3",                        // keys are case-insensitive
		"ver= 1.5.0 ",                  // values are trimmed
		"fw_id=20241011-114455/1.4.4-g6d2a586",
		"app=PlusI4",
		"gen=notanint", // ignored, keeps previous parse
		"malformed",    // no '=', skipped
	}

	d, ok := deviceFromEntry(entry)
	if !ok {
		t.Fatal("deviceFromEntry returned ok=false")
	}
	if d.Info.ID != "shellyplusi4-cafe00112233" {
		t.Errorf("Info.ID = %q, want TXT id to win over instance name", d.Info.ID)
	}
	if d.Info.Gen != 3 {
		t.Errorf("Info.Gen = %d, want 3", d.Info.Gen)
	}
	if d.Info.Version != "1.5.0" {
		t.Errorf("Info.Version = %q, want trimmed 1.5.0", d.Info.Version)
	}
	if d.Info.FirmwareID != "20241011-114455/1.4.4-g6d2a586" {
		t.Errorf("Info.FirmwareID = %q", d.Info.FirmwareID)
	}
	if d.Info.App != "PlusI4" {
		t.Errorf("Info.App = %q, want PlusI4", d.Info.App)
	}
}

func TestDedupDevices(t *testing.T) {
	byID := func(id, addr string) Device {
		return Device{Addr: addr, Info: DeviceInfo{ID: id}, Source: "mdns"}
	}
	devs := []Device{
		byID("shelly-a", "192.168.1.10"),
		byID("shelly-b", "192.168.1.11"),
		byID("shelly-a", "192.168.1.99"), // same id, different addr: dropped
		{Addr: "192.168.1.20"},           // no id, keyed by addr
		{Addr: "192.168.1.20"},           // duplicate addr key: dropped
		{Addr: "192.168.1.21"},
	}

	got := dedupDevices(devs)
	want := []Device{
		byID("shelly-a", "192.168.1.10"),
		byID("shelly-b", "192.168.1.11"),
		{Addr: "192.168.1.20"},
		{Addr: "192.168.1.21"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedupDevices = %+v, want %+v", got, want)
	}
}

func TestDedupDevicesEmpty(t *testing.T) {
	if got := dedupDevices(nil); len(got) != 0 {
		t.Errorf("dedupDevices(nil) = %+v, want empty", got)
	}
}
