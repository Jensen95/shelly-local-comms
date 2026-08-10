package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// statusMsg surfaces a one-line message in the root status bar.
type statusMsg struct {
	text  string
	isErr bool
}

func statusCmd(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: text, isErr: isErr} }
}

type discoverDoneMsg struct {
	found []shelly.Device
	err   error
}

type deviceAddedMsg struct {
	dev shelly.Device
	err error
}

type deviceRemovedMsg struct {
	key string
	err error
}

type probeDoneMsg struct {
	stats []app.LatencyStats
}

type linkSavedMsg struct {
	link app.Link
	err  error
}

type linkDeletedMsg struct {
	id  string
	err error
}

type linkDeployedMsg struct {
	id  string
	err error
}

type previewReadyMsg struct {
	id     string
	name   string
	script string
	err    error
}

type mqttAppliedMsg struct{ err error }

type bleAppliedMsg struct{ err error }

type extenderToggledMsg struct {
	key    string
	enable bool
	err    error
}

type extenderJoinedMsg struct {
	edgeKey     string
	extenderKey string
	err         error
}

type extenderSuggestMsg struct {
	suggestions []app.ExtenderSuggestion
	err         error
}
