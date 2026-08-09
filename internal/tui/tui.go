// Package tui implements the Bubble Tea terminal user interface of
// shellyctl. It is built exclusively against the app.Manager facade; all
// manager calls that touch the network run inside tea.Cmds so the UI
// never blocks.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
)

// Run starts the terminal UI over the given manager and blocks until the
// user quits.
func Run(m app.Manager) error {
	p := tea.NewProgram(New(m), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type tabID int

const (
	tabDevices tabID = iota
	tabLinks
	tabHealth
	tabSettings
	tabCount
)

var tabTitles = [tabCount]string{"1:Devices", "2:Links", "3:Health", "4:Settings"}

// Model is the root Bubble Tea model. It is exported so tests can
// construct it via New with a fake Manager and drive Update/View directly.
type Model struct {
	mgr    app.Manager
	active tabID

	width, height int
	help          help.Model

	status    string
	statusErr bool

	devices  devicesModel
	links    linksModel
	health   healthModel
	settings settingsModel
}

// New builds the root model for the given manager.
func New(mgr app.Manager) Model {
	return Model{
		mgr:      mgr,
		help:     help.New(),
		status:   "ready — press ? for help",
		devices:  newDevicesModel(mgr),
		links:    newLinksModel(mgr),
		health:   newHealthModel(mgr),
		settings: newSettingsModel(mgr),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

func (m Model) capturing() bool {
	switch m.active {
	case tabDevices:
		return m.devices.capturing()
	case tabLinks:
		return m.links.capturing()
	case tabHealth:
		return m.health.capturing()
	case tabSettings:
		return m.settings.capturing()
	}
	return false
}

func (m *Model) switchTab(t tabID) {
	m.active = t
	switch t {
	case tabDevices:
		m.devices.refresh()
	case tabLinks:
		m.links.refresh()
	case tabHealth:
		m.health.refresh()
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		content := msg.Height - 6 // tab bar, spacing, status, help
		m.devices.setSize(msg.Width, content)
		m.links.setSize(msg.Width, content)
		m.health.setSize(msg.Width, content)
		m.settings.setSize(msg.Width, content)
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if !m.capturing() {
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "tab":
				m.switchTab((m.active + 1) % tabCount)
				return m, nil
			case "shift+tab":
				m.switchTab((m.active + tabCount - 1) % tabCount)
				return m, nil
			case "1":
				m.switchTab(tabDevices)
				return m, nil
			case "2":
				m.switchTab(tabLinks)
				return m, nil
			case "3":
				m.switchTab(tabHealth)
				return m, nil
			case "4":
				m.switchTab(tabSettings)
				return m, nil
			case "?":
				m.help.ShowAll = !m.help.ShowAll
				return m, nil
			}
		}
		// Route the key to the active tab only.
		var cmd tea.Cmd
		switch m.active {
		case tabDevices:
			m.devices, cmd = m.devices.update(msg)
		case tabLinks:
			m.links, cmd = m.links.update(msg)
		case tabHealth:
			m.health, cmd = m.health.update(msg)
		case tabSettings:
			m.settings, cmd = m.settings.update(msg)
		}
		return m, cmd

	default:
		m.applyStatus(msg)
		// Broadcast non-key messages (async results, spinner ticks) to
		// every tab; each handles only the types it cares about.
		var cmds []tea.Cmd
		var cmd tea.Cmd
		m.devices, cmd = m.devices.update(msg)
		cmds = append(cmds, cmd)
		m.links, cmd = m.links.update(msg)
		cmds = append(cmds, cmd)
		m.health, cmd = m.health.update(msg)
		cmds = append(cmds, cmd)
		m.settings, cmd = m.settings.update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
}

// applyStatus turns async result messages into the status bar line.
func (m *Model) applyStatus(msg tea.Msg) {
	set := func(text string, err error) {
		if err != nil {
			m.status = text + " failed: " + err.Error()
			m.statusErr = true
			return
		}
		m.status = text
		m.statusErr = false
	}
	switch msg := msg.(type) {
	case statusMsg:
		m.status, m.statusErr = msg.text, msg.isErr
	case discoverDoneMsg:
		if msg.err != nil {
			set("discovery", msg.err)
		} else {
			set(fmt.Sprintf("discovery finished: %d new device(s)", len(msg.found)), nil)
		}
	case deviceAddedMsg:
		if msg.err != nil {
			set("add device", msg.err)
		} else {
			set("added device "+msg.dev.Key(), nil)
		}
	case deviceRemovedMsg:
		if msg.err != nil {
			set("remove device", msg.err)
		} else {
			set("removed device "+msg.key, nil)
		}
	case probeDoneMsg:
		set(fmt.Sprintf("probe complete: %d device(s)", len(msg.stats)), nil)
	case linkSavedMsg:
		if msg.err != nil {
			set("save link", msg.err)
		} else {
			set(fmt.Sprintf("link %q saved", msg.link.Name), nil)
		}
	case linkDeletedMsg:
		if msg.err != nil {
			set("delete link", msg.err)
		} else {
			set("link deleted", nil)
		}
	case linkDeployedMsg:
		if msg.err != nil {
			set("deploy link", msg.err)
		} else {
			set("link deployed to source device", nil)
		}
	case previewReadyMsg:
		if msg.err != nil {
			set("render script", msg.err)
		}
	case mqttAppliedMsg:
		if msg.err != nil {
			set("apply MQTT", msg.err)
		} else {
			set("MQTT settings applied to all devices", nil)
		}
	case bleAppliedMsg:
		if msg.err != nil {
			set("apply Bluetooth", msg.err)
		} else {
			set("Bluetooth settings applied to all devices", nil)
		}
	case extenderToggledMsg:
		if msg.err != nil {
			set("range extender", msg.err)
		} else {
			verb := "enabled"
			if !msg.enable {
				verb = "disabled"
			}
			set(fmt.Sprintf("range extender %s on %s", verb, msg.key), nil)
		}
	case extenderJoinedMsg:
		if msg.err != nil {
			set("join extender", msg.err)
		} else {
			set(fmt.Sprintf("%s joined extender %s", msg.edgeKey, msg.extenderKey), nil)
		}
	}
}

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder

	// Tab bar.
	parts := make([]string, 0, tabCount)
	for i, t := range tabTitles {
		if tabID(i) == m.active {
			parts = append(parts, styleTabActive.Render(t))
		} else {
			parts = append(parts, styleTabInactive.Render(t))
		}
	}
	b.WriteString(strings.Join(parts, " "))
	b.WriteString("\n\n")

	switch m.active {
	case tabDevices:
		b.WriteString(m.devices.view())
	case tabLinks:
		b.WriteString(m.links.view())
	case tabHealth:
		b.WriteString(m.health.view())
	case tabSettings:
		b.WriteString(m.settings.view())
	}
	b.WriteString("\n\n")

	if m.statusErr {
		b.WriteString(styleStatusErr.Render("! " + m.status))
	} else {
		b.WriteString(styleStatusOK.Render("· " + m.status))
	}
	b.WriteString("\n")
	b.WriteString(m.help.View(m.helpKeys()))
	return b.String()
}

// fmtMs renders a duration as fractional milliseconds.
func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}

// --- help footer ---

type helpKeys struct {
	local  []key.Binding
	global []key.Binding
}

func (h helpKeys) ShortHelp() []key.Binding {
	return append(append([]key.Binding{}, h.local...), h.global...)
}

func (h helpKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{h.local, h.global}
}

func bind(keys, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys), key.WithHelp(keys, desc))
}

func (m Model) helpKeys() helpKeys {
	global := []key.Binding{
		bind("tab", "next tab"),
		bind("1-4", "tab"),
		bind("?", "help"),
		bind("q", "quit"),
	}
	var local []key.Binding
	switch m.active {
	case tabDevices:
		local = []key.Binding{
			bind("d", "discover"),
			bind("a", "add"),
			bind("x", "remove"),
			bind("p", "probe"),
			bind("r", "refresh"),
		}
	case tabLinks:
		local = []key.Binding{
			bind("n", "new link"),
			bind("enter", "deploy"),
			bind("v", "preview script"),
			bind("x", "delete"),
			bind("r", "refresh"),
		}
	case tabHealth:
		local = []key.Binding{
			bind("p", "probe now"),
			bind("r", "refresh"),
		}
	case tabSettings:
		local = []key.Binding{
			bind("up/down", "select"),
			bind("enter", "open/apply"),
			bind("esc", "back"),
		}
	}
	return helpKeys{local: local, global: global}
}
