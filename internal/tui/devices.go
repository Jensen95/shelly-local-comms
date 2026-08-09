package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

// discoverTimeoutSeconds is how long a `d`-triggered mDNS scan runs.
const discoverTimeoutSeconds = 5

type devicesMode int

const (
	devModeTable devicesMode = iota
	devModeAdd
	devModeConfirmRemove
)

type devicesModel struct {
	mgr  app.Manager
	tbl  table.Model
	spin spinner.Model
	busy bool
	// busyText labels what the spinner is waiting for.
	busyText string
	mode     devicesMode

	// add-device form: [0] address, [1] password.
	addInputs []textField
	addFocus  int
	addErr    string

	// pending removal.
	removeKey  string
	removeName string

	// rowKeys maps table row index -> Device.Key().
	rowKeys []string
}

func newDevicesModel(mgr app.Manager) devicesModel {
	cols := []table.Column{
		{Title: "Name/ID", Width: 22},
		{Title: "Address", Width: 16},
		{Title: "Model", Width: 14},
		{Title: "Gen", Width: 3},
		{Title: "EWMA ms", Width: 8},
		{Title: "Status", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	tbl.SetStyles(newTableStyles())

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCursor

	addr := newTextField()
	addr.Placeholder = "192.168.1.50"

	pass := newTextField()
	pass.Placeholder = "(optional)"
	pass.Masked = true

	d := devicesModel{
		mgr:       mgr,
		tbl:       tbl,
		spin:      sp,
		addInputs: []textField{addr, pass},
	}
	d.refresh()
	return d
}

func (d devicesModel) capturing() bool { return d.mode != devModeTable }

func (d *devicesModel) setSize(_, h int) {
	if h < 5 {
		h = 5
	}
	d.tbl.SetHeight(h - 3)
}

// refresh rebuilds the table rows from the manager's device registry
// joined with the latest latency stats.
func (d *devicesModel) refresh() {
	stats := map[string]app.LatencyStats{}
	for _, s := range d.mgr.LatencyStats() {
		stats[s.Device] = s
	}
	devs := d.mgr.Devices()
	rows := make([]table.Row, 0, len(devs))
	d.rowKeys = d.rowKeys[:0]
	for _, dev := range devs {
		name := dev.Info.Name
		if name == "" {
			name = dev.Key()
		}
		model := dev.Info.Model
		if model == "" {
			model = "-"
		}
		gen := "-"
		if dev.Info.Gen > 0 {
			gen = strconv.Itoa(dev.Info.Gen)
		}
		lat := "-"
		status := "unknown"
		if s, ok := stats[dev.Key()]; ok && (s.Samples > 0 || s.Failures > 0) {
			if s.Samples > 0 {
				lat = fmtMs(s.EWMA)
			}
			if s.Degraded {
				status = "DEGRADED"
			} else {
				status = "ok"
			}
		}
		rows = append(rows, table.Row{name, dev.Addr, model, gen, lat, status})
		d.rowKeys = append(d.rowKeys, dev.Key())
	}
	d.tbl.SetRows(rows)
	if c := d.tbl.Cursor(); c >= len(rows) && len(rows) > 0 {
		d.tbl.SetCursor(len(rows) - 1)
	}
}

func (d devicesModel) update(msg tea.Msg) (devicesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !d.busy {
			return d, nil
		}
		var cmd tea.Cmd
		d.spin, cmd = d.spin.Update(msg)
		return d, cmd
	case discoverDoneMsg, deviceAddedMsg, deviceRemovedMsg:
		d.busy = false
		d.busyText = ""
		d.refresh()
		return d, nil
	case probeDoneMsg:
		d.busy = false
		d.busyText = ""
		d.refresh()
		return d, nil
	case tea.KeyMsg:
		switch d.mode {
		case devModeAdd:
			return d.updateAdd(msg)
		case devModeConfirmRemove:
			return d.updateConfirm(msg)
		default:
			return d.updateTable(msg)
		}
	}
	return d, nil
}

func (d devicesModel) updateTable(msg tea.KeyMsg) (devicesModel, tea.Cmd) {
	switch msg.String() {
	case "d":
		if d.busy {
			return d, nil
		}
		d.busy = true
		d.busyText = "discovering devices via mDNS..."
		mgr := d.mgr
		return d, tea.Batch(d.spin.Tick, func() tea.Msg {
			found, err := mgr.Discover(context.Background(), discoverTimeoutSeconds)
			return discoverDoneMsg{found: found, err: err}
		})
	case "a":
		d.mode = devModeAdd
		d.addFocus = 0
		d.addErr = ""
		for i := range d.addInputs {
			d.addInputs[i].SetValue("")
			d.addInputs[i].Blur()
		}
		d.addInputs[0].Focus()
		return d, nil
	case "x":
		if c := d.tbl.Cursor(); c >= 0 && c < len(d.rowKeys) {
			d.removeKey = d.rowKeys[c]
			d.removeName = d.tbl.SelectedRow()[0]
			d.mode = devModeConfirmRemove
		}
		return d, nil
	case "p":
		if d.busy {
			return d, nil
		}
		d.busy = true
		d.busyText = "probing devices..."
		mgr := d.mgr
		return d, tea.Batch(d.spin.Tick, func() tea.Msg {
			return probeDoneMsg{stats: mgr.ProbeNow(context.Background())}
		})
	case "r":
		d.refresh()
		return d, statusCmd("device list refreshed", false)
	}
	var cmd tea.Cmd
	d.tbl, cmd = d.tbl.Update(msg)
	return d, cmd
}

func (d devicesModel) updateAdd(msg tea.KeyMsg) (devicesModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.mode = devModeTable
		return d, nil
	case "tab", "down":
		return d.focusAdd(d.addFocus + 1)
	case "shift+tab", "up":
		return d.focusAdd(d.addFocus - 1)
	case "enter":
		if d.addFocus < len(d.addInputs)-1 {
			return d.focusAdd(d.addFocus + 1)
		}
		addr := strings.TrimSpace(d.addInputs[0].Value())
		if addr == "" {
			d.addErr = "address is required"
			return d.focusAdd(0)
		}
		password := d.addInputs[1].Value()
		d.mode = devModeTable
		d.busy = true
		d.busyText = "contacting " + addr + "..."
		mgr := d.mgr
		return d, tea.Batch(d.spin.Tick, func() tea.Msg {
			dev, err := mgr.AddDevice(context.Background(), addr, password)
			return deviceAddedMsg{dev: dev, err: err}
		})
	}
	var cmd tea.Cmd
	d.addInputs[d.addFocus], cmd = d.addInputs[d.addFocus].Update(msg)
	return d, cmd
}

func (d devicesModel) focusAdd(i int) (devicesModel, tea.Cmd) {
	if i < 0 {
		i = len(d.addInputs) - 1
	}
	i %= len(d.addInputs)
	d.addFocus = i
	for j := range d.addInputs {
		if j == i {
			d.addInputs[j].Focus()
		} else {
			d.addInputs[j].Blur()
		}
	}
	return d, nil
}

func (d devicesModel) updateConfirm(msg tea.KeyMsg) (devicesModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		key := d.removeKey
		d.mode = devModeTable
		d.busy = true
		d.busyText = "removing " + key + "..."
		mgr := d.mgr
		return d, tea.Batch(d.spin.Tick, func() tea.Msg {
			return deviceRemovedMsg{key: key, err: mgr.RemoveDevice(key)}
		})
	case "n", "N", "esc":
		d.mode = devModeTable
	}
	return d, nil
}

func (d devicesModel) view() string {
	switch d.mode {
	case devModeAdd:
		var b strings.Builder
		b.WriteString(styleTitle.Render("Add device manually") + "\n\n")
		labels := []string{"Address (IP or hostname)", "Password"}
		for i, in := range d.addInputs {
			cursor := "  "
			if i == d.addFocus {
				cursor = styleCursor.Render("> ")
			}
			b.WriteString(cursor + styleLabel.Render(labels[i]) + "\n  " + in.View() + "\n")
		}
		if d.addErr != "" {
			b.WriteString("\n" + styleErrText.Render(d.addErr) + "\n")
		}
		b.WriteString("\n" + styleHint.Render("enter: next/submit · tab: next field · esc: cancel"))
		return b.String()
	case devModeConfirmRemove:
		return fmt.Sprintf("%s\n\n%s",
			styleTitle.Render("Remove device"),
			styleErrText.Render(fmt.Sprintf("Remove %s (%s)? [y/n]", d.removeName, d.removeKey)))
	}
	var b strings.Builder
	b.WriteString(d.tbl.View())
	if d.busy {
		b.WriteString("\n" + d.spin.View() + styleHint.Render(d.busyText))
	}
	return b.String()
}
