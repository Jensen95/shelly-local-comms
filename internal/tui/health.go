package tui

import (
	"context"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

const healthLegend = "Degraded devices (high latency or repeated probe failures) push their deployed links toward the BLE fallback: the generated scripts tighten the LAN timeout toward its floor so failover stays fast."

type healthModel struct {
	mgr  app.Manager
	tbl  table.Model
	spin spinner.Model
	busy bool
}

func newHealthModel(mgr app.Manager) healthModel {
	cols := []table.Column{
		{Title: "Device", Width: 22},
		{Title: "Last ms", Width: 8},
		{Title: "EWMA ms", Width: 8},
		{Title: "Samples", Width: 7},
		{Title: "Failures", Width: 8},
		{Title: "Degraded", Width: 8},
		{Title: "Last error", Width: 28},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	tbl.SetStyles(newTableStyles())

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCursor

	h := healthModel{mgr: mgr, tbl: tbl, spin: sp}
	h.refresh()
	return h
}

func (h healthModel) capturing() bool { return false }

func (h *healthModel) setSize(w, ht int) {
	if ht < 7 {
		ht = 7
	}
	h.tbl.SetHeight(ht - 5)
}

func (h *healthModel) refresh() {
	stats := h.mgr.LatencyStats()
	rows := make([]table.Row, 0, len(stats))
	for _, s := range stats {
		last, ewma := "-", "-"
		if s.Samples > 0 {
			last = fmtMs(s.Last)
			ewma = fmtMs(s.EWMA)
		}
		degraded := "no"
		if s.Degraded {
			degraded = "yes"
		}
		rows = append(rows, table.Row{
			s.Device, last, ewma,
			strconv.Itoa(s.Samples), strconv.Itoa(s.Failures),
			degraded, s.LastError,
		})
	}
	h.tbl.SetRows(rows)
	if c := h.tbl.Cursor(); c >= len(rows) && len(rows) > 0 {
		h.tbl.SetCursor(len(rows) - 1)
	}
}

func (h healthModel) update(msg tea.Msg) (healthModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !h.busy {
			return h, nil
		}
		var cmd tea.Cmd
		h.spin, cmd = h.spin.Update(msg)
		return h, cmd
	case probeDoneMsg:
		h.busy = false
		h.refresh()
		return h, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "p":
			if h.busy {
				return h, nil
			}
			h.busy = true
			mgr := h.mgr
			return h, tea.Batch(h.spin.Tick, func() tea.Msg {
				return probeDoneMsg{stats: mgr.ProbeNow(context.Background())}
			})
		case "r":
			h.refresh()
			return h, statusCmd("health view refreshed", false)
		}
		var cmd tea.Cmd
		h.tbl, cmd = h.tbl.Update(msg)
		return h, cmd
	}
	return h, nil
}

func (h healthModel) view() string {
	var b strings.Builder
	b.WriteString(h.tbl.View() + "\n")
	if h.busy {
		b.WriteString(h.spin.View() + styleHint.Render("probing...") + "\n")
	}
	b.WriteString(styleLegend.Render(healthLegend))
	return b.String()
}
