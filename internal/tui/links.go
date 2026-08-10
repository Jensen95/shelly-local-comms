package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// linkEvents are the source input events the wizard cycles through.
var linkEvents = []string{"toggle", "single_push", "double_push", "long_push"}

type linksMode int

const (
	lnkModeTable linksMode = iota
	lnkModeWizard
	lnkModeConfirmDelete
	lnkModePreview
)

// Wizard steps, in order.
const (
	wStepName = iota
	wStepSource
	wStepComponent
	wStepEvent
	wStepTarget
	wStepMethod
	wStepParams
	wStepBLE
	wStepStrategy
	wStepCount
)

type linkWizard struct {
	step      int
	devices   []shelly.Device
	name      textField
	srcCursor int
	tgtCursor int
	component textField
	eventIdx  int
	method    textField
	params    textField
	ble       bool
	race      bool
	errText   string
}

func newLinkWizard(devices []shelly.Device) linkWizard {
	mk := func(value, placeholder string) textField {
		ti := newTextField()
		ti.SetValue(value)
		ti.Placeholder = placeholder
		return ti
	}
	w := linkWizard{
		devices:   devices,
		name:      mk("", "hall switch to lamp"),
		component: mk("input:0", "input:0"),
		method:    mk("Switch.Toggle", "Switch.Toggle"),
		params:    mk(`{"id":0}`, `{"id":0}`),
		ble:       true,
	}
	w.name.Focus()
	return w
}

type linksModel struct {
	mgr  app.Manager
	tbl  table.Model
	spin spinner.Model
	busy bool

	mode linksMode
	wiz  linkWizard

	vp           viewport.Model
	previewTitle string

	deleteID   string
	deleteName string

	// rowIDs maps table row index -> Link.ID.
	rowIDs []string

	width, height int
}

func newLinksModel(mgr app.Manager) linksModel {
	cols := []table.Column{
		{Title: "Name", Width: 18},
		{Title: "Source (dev comp event)", Width: 38},
		{Title: "Target (dev method)", Width: 32},
		{Title: "BLE", Width: 3},
		{Title: "Script", Width: 12},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(10))
	tbl.SetStyles(newTableStyles())

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCursor

	l := linksModel{
		mgr:  mgr,
		tbl:  tbl,
		spin: sp,
		vp:   viewport.New(80, 20),
	}
	l.refresh()
	return l
}

func (l linksModel) capturing() bool { return l.mode != lnkModeTable }

func (l *linksModel) setSize(w, h int) {
	if h < 5 {
		h = 5
	}
	l.width, l.height = w, h
	l.tbl.SetHeight(h - 3)
	l.vp.Width = w
	l.vp.Height = h - 3
	if l.vp.Height < 3 {
		l.vp.Height = 3
	}
}

func (l *linksModel) refresh() {
	links := l.mgr.Links()
	rows := make([]table.Row, 0, len(links))
	l.rowIDs = l.rowIDs[:0]
	for _, lk := range links {
		source := fmt.Sprintf("%s %s %s", lk.SourceDevice, lk.SourceComponent, lk.SourceEvent)
		target := fmt.Sprintf("%s %s", lk.TargetDevice, lk.TargetMethod)
		ble := "off"
		if lk.Fallback.BLEEnabled {
			ble = "on"
			if lk.Fallback.Strategy == app.StrategyRace {
				ble = "race"
			}
		}
		script := "not deployed"
		if lk.DeployedScriptID != 0 {
			script = fmt.Sprintf("script #%d", lk.DeployedScriptID)
		}
		rows = append(rows, table.Row{lk.Name, source, target, ble, script})
		l.rowIDs = append(l.rowIDs, lk.ID)
	}
	l.tbl.SetRows(rows)
	if c := l.tbl.Cursor(); c >= len(rows) && len(rows) > 0 {
		l.tbl.SetCursor(len(rows) - 1)
	}
}

func (l linksModel) selectedID() (string, bool) {
	if c := l.tbl.Cursor(); c >= 0 && c < len(l.rowIDs) {
		return l.rowIDs[c], true
	}
	return "", false
}

func (l linksModel) update(msg tea.Msg) (linksModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !l.busy {
			return l, nil
		}
		var cmd tea.Cmd
		l.spin, cmd = l.spin.Update(msg)
		return l, cmd
	case linkSavedMsg, linkDeletedMsg, linkDeployedMsg:
		l.busy = false
		l.refresh()
		return l, nil
	case previewReadyMsg:
		l.busy = false
		if msg.err == nil {
			l.mode = lnkModePreview
			l.previewTitle = msg.name
			l.vp.SetContent(msg.script)
			l.vp.GotoTop()
		}
		return l, nil
	case tea.KeyMsg:
		switch l.mode {
		case lnkModeWizard:
			return l.updateWizard(msg)
		case lnkModeConfirmDelete:
			return l.updateConfirm(msg)
		case lnkModePreview:
			if msg.String() == "esc" || msg.String() == "q" {
				l.mode = lnkModeTable
				return l, nil
			}
			var cmd tea.Cmd
			l.vp, cmd = l.vp.Update(msg)
			return l, cmd
		default:
			return l.updateTable(msg)
		}
	}
	return l, nil
}

func (l linksModel) updateTable(msg tea.KeyMsg) (linksModel, tea.Cmd) {
	switch msg.String() {
	case "n":
		devices := l.mgr.Devices()
		if len(devices) == 0 {
			return l, statusCmd("no devices known — run discovery first", true)
		}
		l.mode = lnkModeWizard
		l.wiz = newLinkWizard(devices)
		return l, nil
	case "enter", "D":
		id, ok := l.selectedID()
		if !ok || l.busy {
			return l, nil
		}
		l.busy = true
		mgr := l.mgr
		return l, tea.Batch(l.spin.Tick, func() tea.Msg {
			return linkDeployedMsg{id: id, err: mgr.DeployLink(context.Background(), id)}
		})
	case "v":
		id, ok := l.selectedID()
		if !ok {
			return l, nil
		}
		name := l.tbl.SelectedRow()[0]
		mgr := l.mgr
		return l, func() tea.Msg {
			script, err := mgr.RenderLinkScript(id)
			return previewReadyMsg{id: id, name: name, script: script, err: err}
		}
	case "x":
		id, ok := l.selectedID()
		if !ok {
			return l, nil
		}
		l.deleteID = id
		l.deleteName = l.tbl.SelectedRow()[0]
		l.mode = lnkModeConfirmDelete
		return l, nil
	case "r":
		l.refresh()
		return l, statusCmd("link list refreshed", false)
	}
	var cmd tea.Cmd
	l.tbl, cmd = l.tbl.Update(msg)
	return l, cmd
}

func (l linksModel) updateConfirm(msg tea.KeyMsg) (linksModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		id := l.deleteID
		l.mode = lnkModeTable
		l.busy = true
		mgr := l.mgr
		return l, tea.Batch(l.spin.Tick, func() tea.Msg {
			return linkDeletedMsg{id: id, err: mgr.DeleteLink(context.Background(), id)}
		})
	case "n", "N", "esc":
		l.mode = lnkModeTable
	}
	return l, nil
}

func (l linksModel) updateWizard(msg tea.KeyMsg) (linksModel, tea.Cmd) {
	w := &l.wiz
	switch msg.String() {
	case "esc":
		l.mode = lnkModeTable
		return l, nil
	case "shift+tab":
		if w.step > 0 {
			w.step--
			l.syncWizardFocus()
		}
		return l, nil
	case "enter":
		if w.step < wStepStrategy {
			w.step++
			l.syncWizardFocus()
			return l, nil
		}
		return l.finishWizard()
	}

	switch w.step {
	case wStepSource:
		switch msg.String() {
		case "up", "k":
			if w.srcCursor > 0 {
				w.srcCursor--
			}
		case "down", "j":
			if w.srcCursor < len(w.devices)-1 {
				w.srcCursor++
			}
		}
		return l, nil
	case wStepTarget:
		switch msg.String() {
		case "up", "k":
			if w.tgtCursor > 0 {
				w.tgtCursor--
			}
		case "down", "j":
			if w.tgtCursor < len(w.devices)-1 {
				w.tgtCursor++
			}
		}
		return l, nil
	case wStepEvent:
		switch msg.String() {
		case "down", "j", "right", "l":
			w.eventIdx = (w.eventIdx + 1) % len(linkEvents)
		case "up", "k", "left", "h":
			w.eventIdx = (w.eventIdx + len(linkEvents) - 1) % len(linkEvents)
		}
		return l, nil
	case wStepBLE:
		switch msg.String() {
		case " ", "left", "right", "h", "l":
			w.ble = !w.ble
		}
		return l, nil
	case wStepStrategy:
		switch msg.String() {
		case " ", "left", "right", "h", "l":
			w.race = !w.race
		}
		return l, nil
	}

	var cmd tea.Cmd
	switch w.step {
	case wStepName:
		w.name, cmd = w.name.Update(msg)
	case wStepComponent:
		w.component, cmd = w.component.Update(msg)
	case wStepMethod:
		w.method, cmd = w.method.Update(msg)
	case wStepParams:
		w.params, cmd = w.params.Update(msg)
	}
	return l, cmd
}

// syncWizardFocus focuses the text field of the current step (if any).
func (l *linksModel) syncWizardFocus() {
	w := &l.wiz
	inputs := []*textField{&w.name, &w.component, &w.method, &w.params}
	for _, in := range inputs {
		in.Blur()
	}
	switch w.step {
	case wStepName:
		w.name.Focus()
	case wStepComponent:
		w.component.Focus()
	case wStepMethod:
		w.method.Focus()
	case wStepParams:
		w.params.Focus()
	}
}

func (l linksModel) finishWizard() (linksModel, tea.Cmd) {
	w := &l.wiz

	var params map[string]any
	raw := strings.TrimSpace(w.params.Value())
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			w.errText = "invalid params JSON: " + err.Error()
			w.step = wStepParams
			l.syncWizardFocus()
			return l, nil
		}
	}

	src := w.devices[w.srcCursor].Key()
	tgt := w.devices[w.tgtCursor].Key()
	component := strings.TrimSpace(w.component.Value())
	if component == "" {
		component = "input:0"
	}
	method := strings.TrimSpace(w.method.Value())
	if method == "" {
		method = "Switch.Toggle"
	}
	name := strings.TrimSpace(w.name.Value())
	if name == "" {
		name = src + " -> " + tgt
	}
	fb := app.DefaultFallback()
	fb.BLEEnabled = w.ble
	fb.Strategy = app.StrategyFallback
	if w.race {
		fb.Strategy = app.StrategyRace
		if app.NonIdempotentMethod(method) {
			w.errText = "race strategy needs an idempotent method (a Toggle over both LAN and BLE undoes itself) — use e.g. Switch.Set, or the fallback strategy"
			w.step = wStepMethod
			l.syncWizardFocus()
			return l, nil
		}
	}

	link := app.Link{
		Name:            name,
		SourceDevice:    src,
		SourceComponent: component,
		SourceEvent:     linkEvents[w.eventIdx],
		TargetDevice:    tgt,
		TargetMethod:    method,
		TargetParams:    params,
		Fallback:        fb,
	}

	l.mode = lnkModeTable
	l.busy = true
	mgr := l.mgr
	return l, tea.Batch(l.spin.Tick, func() tea.Msg {
		saved, err := mgr.SaveLink(link)
		return linkSavedMsg{link: saved, err: err}
	})
}

func (l linksModel) view() string {
	switch l.mode {
	case lnkModeWizard:
		return l.viewWizard()
	case lnkModeConfirmDelete:
		return fmt.Sprintf("%s\n\n%s",
			styleTitle.Render("Delete link"),
			styleErrText.Render(fmt.Sprintf("Delete link %q? This does not undeploy the script. [y/n]", l.deleteName)))
	case lnkModePreview:
		var b strings.Builder
		b.WriteString(styleTitle.Render("Script preview: "+l.previewTitle) + "\n")
		b.WriteString(l.vp.View() + "\n")
		b.WriteString(styleHint.Render("up/down: scroll · esc: close"))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(l.tbl.View())
	if l.busy {
		b.WriteString("\n" + l.spin.View() + styleHint.Render("working..."))
	}
	return b.String()
}

func (l linksModel) viewWizard() string {
	w := l.wiz
	var b strings.Builder
	b.WriteString(styleTitle.Render("New link") + "\n\n")

	line := func(step int, label, widget string) {
		cursor := "  "
		if w.step == step {
			cursor = styleCursor.Render("> ")
		}
		b.WriteString(cursor + styleLabel.Render(label) + " " + widget + "\n")
	}

	deviceWidget := func(active bool, cursor int) string {
		if !active {
			if cursor >= 0 && cursor < len(w.devices) {
				return deviceLabel(w.devices[cursor])
			}
			return "-"
		}
		var s strings.Builder
		s.WriteString("\n")
		for i, dev := range w.devices {
			marker := "    "
			label := deviceLabel(dev)
			if i == cursor {
				marker = "  " + styleCursor.Render("* ")
				label = styleCursor.Render(label)
			}
			s.WriteString(marker + label + "\n")
		}
		return s.String()
	}

	line(wStepName, "Name:", w.name.View())
	line(wStepSource, "Source device:", deviceWidget(w.step == wStepSource, w.srcCursor))
	line(wStepComponent, "Source component:", w.component.View())
	event := linkEvents[w.eventIdx]
	if w.step == wStepEvent {
		event = styleCursor.Render("< " + event + " >")
	}
	line(wStepEvent, "Source event:", event)
	line(wStepTarget, "Target device:", deviceWidget(w.step == wStepTarget, w.tgtCursor))
	line(wStepMethod, "Target method:", w.method.View())
	line(wStepParams, "Params JSON:", w.params.View())
	ble := "off"
	if w.ble {
		ble = "on"
	}
	if w.step == wStepBLE {
		ble = styleCursor.Render("< " + ble + " >")
	}
	line(wStepBLE, "BLE fallback:", ble)
	strategy := "fallback (LAN first, BLE on failure)"
	if w.race {
		strategy = "race (LAN + BLE together, idempotent methods only)"
	}
	if w.step == wStepStrategy {
		strategy = styleCursor.Render("< " + strategy + " >")
	}
	line(wStepStrategy, "Strategy:", strategy)

	if w.errText != "" {
		b.WriteString("\n" + styleErrText.Render(w.errText) + "\n")
	}
	b.WriteString("\n" + styleHint.Render("enter: next/save · shift+tab: back · up/down: choose · space: toggle · esc: cancel"))
	return b.String()
}

func deviceLabel(dev shelly.Device) string {
	if dev.Info.Name != "" {
		return fmt.Sprintf("%s (%s)", dev.Info.Name, dev.Key())
	}
	return fmt.Sprintf("%s (%s)", dev.Key(), dev.Addr)
}
