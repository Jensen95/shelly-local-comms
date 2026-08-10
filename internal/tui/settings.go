package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

type settingsMode int

const (
	setModeMenu settingsMode = iota
	setModeMQTT
	setModeBLE
	setModeExtMenu
	setModeExtToggle
	setModeExtPairExtender
	setModeExtPairEdge
)

// Sections in the settings menu.
const (
	sectionMQTT = iota
	sectionBLE
	sectionExtender
	sectionCount
)

// MQTT form item indexes.
const (
	mqttItemEnable = iota
	mqttItemServer
	mqttItemUser
	mqttItemPassword
	mqttItemPrefix
	mqttItemRPCNtf
	mqttItemStatusNtf
	mqttItemApply
	mqttItemCount
)

// BLE form item indexes.
const (
	bleItemEnable = iota
	bleItemRPC
	bleItemObserver
	bleItemApply
	bleItemCount
)

type settingsModel struct {
	mgr  app.Manager
	mode settingsMode
	spin spinner.Model
	busy bool

	menuCursor int

	// MQTT form: bool state + text fields [server user pass prefix].
	mqttEnable    bool
	mqttRPCNtf    bool
	mqttStatusNtf bool
	mqttInputs    [4]textField
	mqttFocus     int

	// BLE form.
	ble      app.BLESettings
	bleFocus int

	// Range extender flows.
	extMenuCursor   int
	devices         []shelly.Device
	devCursor       int
	pairExtenderKey string

	// Per-section result lines.
	sectionStatus [sectionCount]string
	sectionErr    [sectionCount]bool
}

func newSettingsModel(mgr app.Manager) settingsModel {
	mk := func(placeholder string) textField {
		ti := newTextField()
		ti.Placeholder = placeholder
		return ti
	}
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCursor

	s := settingsModel{mgr: mgr, spin: sp}
	s.mqttInputs[0] = mk("broker.local:1883")
	s.mqttInputs[1] = mk("user (optional)")
	s.mqttInputs[2] = mk("password (optional)")
	s.mqttInputs[2].Masked = true
	s.mqttInputs[3] = mk("topic prefix (optional)")
	s.mqttRPCNtf = true
	s.mqttStatusNtf = true
	s.ble = app.BLESettings{Enable: true, RPC: true}
	return s
}

func (s settingsModel) capturing() bool { return s.mode != setModeMenu }

func (s *settingsModel) setSize(w, h int) {}

func (s settingsModel) update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.busy {
			return s, nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd
	case mqttAppliedMsg:
		s.busy = false
		s.setSectionResult(sectionMQTT, "MQTT settings applied to all devices", msg.err)
		return s, nil
	case bleAppliedMsg:
		s.busy = false
		s.setSectionResult(sectionBLE, "Bluetooth settings applied to all devices", msg.err)
		return s, nil
	case extenderToggledMsg:
		s.busy = false
		verb := "enabled"
		if !msg.enable {
			verb = "disabled"
		}
		s.setSectionResult(sectionExtender, fmt.Sprintf("range extender %s on %s", verb, msg.key), msg.err)
		return s, nil
	case extenderJoinedMsg:
		s.busy = false
		s.setSectionResult(sectionExtender, fmt.Sprintf("%s joined extender %s", msg.edgeKey, msg.extenderKey), msg.err)
		return s, nil
	case extenderSuggestMsg:
		s.busy = false
		s.setSectionResult(sectionExtender, formatSuggestions(msg.suggestions), msg.err)
		return s, nil
	case tea.KeyMsg:
		switch s.mode {
		case setModeMenu:
			return s.updateMenu(msg)
		case setModeMQTT:
			return s.updateMQTT(msg)
		case setModeBLE:
			return s.updateBLE(msg)
		case setModeExtMenu:
			return s.updateExtMenu(msg)
		case setModeExtToggle, setModeExtPairExtender, setModeExtPairEdge:
			return s.updateDevicePick(msg)
		}
	}
	return s, nil
}

func (s *settingsModel) setSectionResult(section int, ok string, err error) {
	if err != nil {
		s.sectionStatus[section] = err.Error()
		s.sectionErr[section] = true
		return
	}
	s.sectionStatus[section] = ok
	s.sectionErr[section] = false
}

func (s settingsModel) updateMenu(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if s.menuCursor > 0 {
			s.menuCursor--
		}
	case "down", "j":
		if s.menuCursor < sectionCount-1 {
			s.menuCursor++
		}
	case "enter":
		switch s.menuCursor {
		case sectionMQTT:
			s.mode = setModeMQTT
			s.mqttFocus = mqttItemEnable
			s.syncMQTTFocus()
		case sectionBLE:
			s.mode = setModeBLE
			s.bleFocus = bleItemEnable
		case sectionExtender:
			s.mode = setModeExtMenu
			s.extMenuCursor = 0
		}
	}
	return s, nil
}

// --- MQTT form ---

// mqttInputIndex maps a form item to its textinput index, or -1.
func mqttInputIndex(item int) int {
	switch item {
	case mqttItemServer:
		return 0
	case mqttItemUser:
		return 1
	case mqttItemPassword:
		return 2
	case mqttItemPrefix:
		return 3
	}
	return -1
}

func (s *settingsModel) syncMQTTFocus() {
	focus := mqttInputIndex(s.mqttFocus)
	for i := range s.mqttInputs {
		if i == focus {
			s.mqttInputs[i].Focus()
		} else {
			s.mqttInputs[i].Blur()
		}
	}
}

func (s settingsModel) updateMQTT(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.mode = setModeMenu
		for i := range s.mqttInputs {
			s.mqttInputs[i].Blur()
		}
		return s, nil
	case "up", "shift+tab":
		if s.mqttFocus > 0 {
			s.mqttFocus--
			s.syncMQTTFocus()
		}
		return s, nil
	case "down", "tab":
		if s.mqttFocus < mqttItemCount-1 {
			s.mqttFocus++
			s.syncMQTTFocus()
		}
		return s, nil
	case " ":
		switch s.mqttFocus {
		case mqttItemEnable:
			s.mqttEnable = !s.mqttEnable
			return s, nil
		case mqttItemRPCNtf:
			s.mqttRPCNtf = !s.mqttRPCNtf
			return s, nil
		case mqttItemStatusNtf:
			s.mqttStatusNtf = !s.mqttStatusNtf
			return s, nil
		}
	case "enter":
		if s.mqttFocus == mqttItemApply {
			return s.applyMQTT()
		}
		s.mqttFocus++
		s.syncMQTTFocus()
		return s, nil
	}
	if idx := mqttInputIndex(s.mqttFocus); idx >= 0 {
		var cmd tea.Cmd
		s.mqttInputs[idx], cmd = s.mqttInputs[idx].Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s settingsModel) applyMQTT() (settingsModel, tea.Cmd) {
	if s.busy {
		return s, nil
	}
	cfg := app.MQTTSettings{
		Enable:       s.mqttEnable,
		Server:       strings.TrimSpace(s.mqttInputs[0].Value()),
		User:         strings.TrimSpace(s.mqttInputs[1].Value()),
		Password:     s.mqttInputs[2].Value(),
		TopicPrefix:  strings.TrimSpace(s.mqttInputs[3].Value()),
		RPCNotifs:    s.mqttRPCNtf,
		StatusNotifs: s.mqttStatusNtf,
	}
	if cfg.Enable && cfg.Server == "" {
		s.setSectionResult(sectionMQTT, "", fmt.Errorf("server (host:port) is required when MQTT is enabled"))
		return s, nil
	}
	s.busy = true
	mgr := s.mgr
	return s, tea.Batch(s.spin.Tick, func() tea.Msg {
		return mqttAppliedMsg{err: mgr.ApplyMQTT(context.Background(), cfg, nil)}
	})
}

// --- BLE form ---

func (s settingsModel) updateBLE(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.mode = setModeMenu
		return s, nil
	case "up", "shift+tab", "k":
		if s.bleFocus > 0 {
			s.bleFocus--
		}
		return s, nil
	case "down", "tab", "j":
		if s.bleFocus < bleItemCount-1 {
			s.bleFocus++
		}
		return s, nil
	case " ":
		switch s.bleFocus {
		case bleItemEnable:
			s.ble.Enable = !s.ble.Enable
		case bleItemRPC:
			s.ble.RPC = !s.ble.RPC
		case bleItemObserver:
			s.ble.Observer = !s.ble.Observer
		}
		return s, nil
	case "enter":
		if s.bleFocus != bleItemApply {
			s.bleFocus++
			return s, nil
		}
		if s.busy {
			return s, nil
		}
		s.busy = true
		cfg := s.ble
		mgr := s.mgr
		return s, tea.Batch(s.spin.Tick, func() tea.Msg {
			return bleAppliedMsg{err: mgr.ApplyBLE(context.Background(), cfg, nil)}
		})
	}
	return s, nil
}

// --- Range extender flows ---

func (s settingsModel) updateExtMenu(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	const extMenuEntries = 3
	switch msg.String() {
	case "esc":
		s.mode = setModeMenu
	case "up", "k":
		if s.extMenuCursor > 0 {
			s.extMenuCursor--
		}
	case "down", "j":
		if s.extMenuCursor < extMenuEntries-1 {
			s.extMenuCursor++
		}
	case "enter":
		if s.extMenuCursor == 2 {
			s.busy = true
			mgr := s.mgr
			return s, tea.Batch(s.spin.Tick, func() tea.Msg {
				sugg, err := mgr.SuggestExtenders(context.Background())
				return extenderSuggestMsg{suggestions: sugg, err: err}
			})
		}
		s.devices = s.mgr.Devices()
		s.devCursor = 0
		if len(s.devices) == 0 {
			s.setSectionResult(sectionExtender, "", fmt.Errorf("no devices known — run discovery first"))
			return s, nil
		}
		if s.extMenuCursor == 0 {
			s.mode = setModeExtToggle
		} else {
			s.mode = setModeExtPairExtender
			s.pairExtenderKey = ""
		}
	}
	return s, nil
}

// formatSuggestions renders extender suggestions into one status line.
func formatSuggestions(sugg []app.ExtenderSuggestion) string {
	if len(sugg) == 0 {
		return "no suggestions: every reachable device has decent WiFi signal"
	}
	parts := make([]string, len(sugg))
	for i, sg := range sugg {
		if sg.Method == app.SuggestBLEProximity {
			parts[i] = fmt.Sprintf("%s (wifi %d dBm) <- %s (closest by BLE: heard at %d dBm; wifi %d dBm)",
				sg.Edge, sg.EdgeRSSI, sg.Extender, sg.BLERSSI, sg.ExtenderRSSI)
		} else {
			parts[i] = fmt.Sprintf("%s (wifi %d dBm) <- %s (wifi %d dBm; fallback: strongest router signal, proximity unknown)",
				sg.Edge, sg.EdgeRSSI, sg.Extender, sg.ExtenderRSSI)
		}
	}
	return "suggestions: " + strings.Join(parts, " · ")
}

func (s settingsModel) updateDevicePick(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.mode = setModeExtMenu
		return s, nil
	case "up", "k":
		if s.devCursor > 0 {
			s.devCursor--
		}
		return s, nil
	case "down", "j":
		if s.devCursor < len(s.devices)-1 {
			s.devCursor++
		}
		return s, nil
	}
	if s.devCursor < 0 || s.devCursor >= len(s.devices) {
		return s, nil
	}
	key := s.devices[s.devCursor].Key()

	switch s.mode {
	case setModeExtToggle:
		switch msg.String() {
		case "enter", "e":
			return s.toggleExtender(key, true)
		case "d":
			return s.toggleExtender(key, false)
		}
	case setModeExtPairExtender:
		if msg.String() == "enter" {
			s.pairExtenderKey = key
			s.mode = setModeExtPairEdge
			s.devCursor = 0
		}
	case setModeExtPairEdge:
		if msg.String() == "enter" {
			if key == s.pairExtenderKey {
				s.setSectionResult(sectionExtender, "", fmt.Errorf("edge device must differ from the extender"))
				return s, nil
			}
			if s.busy {
				return s, nil
			}
			s.busy = true
			s.mode = setModeExtMenu
			edge, ext := key, s.pairExtenderKey
			mgr := s.mgr
			return s, tea.Batch(s.spin.Tick, func() tea.Msg {
				return extenderJoinedMsg{edgeKey: edge, extenderKey: ext, err: mgr.JoinExtender(context.Background(), edge, ext)}
			})
		}
	}
	return s, nil
}

func (s settingsModel) toggleExtender(key string, enable bool) (settingsModel, tea.Cmd) {
	if s.busy {
		return s, nil
	}
	s.busy = true
	s.mode = setModeExtMenu
	mgr := s.mgr
	return s, tea.Batch(s.spin.Tick, func() tea.Msg {
		return extenderToggledMsg{key: key, enable: enable, err: mgr.EnableRangeExtender(context.Background(), key, enable)}
	})
}

// --- views ---

func (s settingsModel) view() string {
	switch s.mode {
	case setModeMQTT:
		return s.viewMQTT()
	case setModeBLE:
		return s.viewBLE()
	case setModeExtMenu:
		return s.viewExtMenu()
	case setModeExtToggle:
		return s.viewDevicePick("Range extender: pick a device",
			"enter/e: enable extender AP · d: disable · esc: back")
	case setModeExtPairExtender:
		return s.viewDevicePick("Pair: pick the extender device (the one with the AP)",
			"enter: choose extender · esc: back")
	case setModeExtPairEdge:
		return s.viewDevicePick(fmt.Sprintf("Pair: pick the edge device to join %s", s.pairExtenderKey),
			"enter: join extender · esc: back")
	}
	return s.viewMenu()
}

func (s settingsModel) viewMenu() string {
	entries := []struct{ title, desc string }{
		{"MQTT", "broker config, applied to all devices"},
		{"Bluetooth", "enable / RPC / observer, applied to all devices"},
		{"Range extender", "enable AP on a device, pair edge devices"},
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("Settings") + "\n\n")
	for i, e := range entries {
		cursor := "  "
		title := e.title
		if i == s.menuCursor {
			cursor = styleCursor.Render("> ")
			title = styleCursor.Render(title)
		}
		fmt.Fprintf(&b, "%s%s — %s\n", cursor, title, styleLabel.Render(e.desc))
		if s.sectionStatus[i] != "" {
			line := "    " + s.sectionStatus[i]
			if s.sectionErr[i] {
				b.WriteString(styleErrText.Render(line) + "\n")
			} else {
				b.WriteString(styleStatusOK.Render(line) + "\n")
			}
		}
	}
	b.WriteString("\n" + styleHint.Render("up/down: select · enter: open"))
	return b.String()
}

func checkbox(label string, on, focused bool) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	line := box + " " + label
	cursor := "  "
	if focused {
		cursor = styleCursor.Render("> ")
		line = styleCursor.Render(line)
	}
	return cursor + line
}

func (s settingsModel) viewMQTT() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("MQTT — apply to all devices") + "\n\n")
	b.WriteString(checkbox("Enable MQTT", s.mqttEnable, s.mqttFocus == mqttItemEnable) + "\n")

	field := func(item int, label string) {
		cursor := "  "
		if s.mqttFocus == item {
			cursor = styleCursor.Render("> ")
		}
		b.WriteString(cursor + styleLabel.Render(label) + " " + s.mqttInputs[mqttInputIndex(item)].View() + "\n")
	}
	field(mqttItemServer, "Server (host:port):")
	field(mqttItemUser, "User:             ")
	field(mqttItemPassword, "Password:         ")
	field(mqttItemPrefix, "Topic prefix:     ")

	b.WriteString(checkbox("RPC notifications (rpc_ntf)", s.mqttRPCNtf, s.mqttFocus == mqttItemRPCNtf) + "\n")
	b.WriteString(checkbox("Status notifications (status_ntf)", s.mqttStatusNtf, s.mqttFocus == mqttItemStatusNtf) + "\n")

	apply := "[ Apply to all devices ]"
	if s.mqttFocus == mqttItemApply {
		apply = styleCursor.Render("> " + apply)
	} else {
		apply = "  " + apply
	}
	b.WriteString("\n" + apply + "\n")
	b.WriteString(s.sectionResultLine(sectionMQTT))
	if s.busy {
		b.WriteString("\n" + s.spin.View() + styleHint.Render("applying..."))
	}
	b.WriteString("\n" + styleHint.Render("up/down: field · space: toggle · enter on button: apply · esc: back"))
	return b.String()
}

func (s settingsModel) viewBLE() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Bluetooth — apply to all devices") + "\n\n")
	b.WriteString(checkbox("Enable Bluetooth", s.ble.Enable, s.bleFocus == bleItemEnable) + "\n")
	b.WriteString(checkbox("BLE RPC (needed for link fallback)", s.ble.RPC, s.bleFocus == bleItemRPC) + "\n")
	b.WriteString(checkbox("Observer mode (relay BLU advertisements)", s.ble.Observer, s.bleFocus == bleItemObserver) + "\n")

	apply := "[ Apply to all devices ]"
	if s.bleFocus == bleItemApply {
		apply = styleCursor.Render("> " + apply)
	} else {
		apply = "  " + apply
	}
	b.WriteString("\n" + apply + "\n")
	b.WriteString(s.sectionResultLine(sectionBLE))
	if s.busy {
		b.WriteString("\n" + s.spin.View() + styleHint.Render("applying..."))
	}
	b.WriteString("\n" + styleHint.Render("up/down: field · space: toggle · enter on button: apply · esc: back"))
	return b.String()
}

func (s settingsModel) viewExtMenu() string {
	entries := []string{
		"Toggle range extender AP on a device",
		"Pair an edge device to an extender",
		"Suggest pairings from WiFi signal strength",
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("Range extender") + "\n\n")
	for i, e := range entries {
		cursor := "  "
		if i == s.extMenuCursor {
			cursor = styleCursor.Render("> ")
			e = styleCursor.Render(e)
		}
		b.WriteString(cursor + e + "\n")
	}
	b.WriteString(s.sectionResultLine(sectionExtender))
	if s.busy {
		b.WriteString("\n" + s.spin.View() + styleHint.Render("working..."))
	}
	b.WriteString("\n" + styleHint.Render("up/down: select · enter: open · esc: back"))
	return b.String()
}

func (s settingsModel) viewDevicePick(title, hint string) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(title) + "\n\n")
	for i, dev := range s.devices {
		cursor := "  "
		label := deviceLabel(dev)
		if i == s.devCursor {
			cursor = styleCursor.Render("> ")
			label = styleCursor.Render(label)
		}
		b.WriteString(cursor + label + "\n")
	}
	b.WriteString(s.sectionResultLine(sectionExtender))
	b.WriteString("\n" + styleHint.Render(hint))
	return b.String()
}

func (s settingsModel) sectionResultLine(section int) string {
	if s.sectionStatus[section] == "" {
		return ""
	}
	line := "\n" + s.sectionStatus[section]
	if s.sectionErr[section] {
		return styleErrText.Render(line)
	}
	return styleStatusOK.Render(line)
}
