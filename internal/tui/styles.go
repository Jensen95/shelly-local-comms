package tui

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

var (
	colorAccent = lipgloss.Color("39")  // bright blue
	colorText   = lipgloss.Color("252") // near-white
	colorDim    = lipgloss.Color("243") // grey
	colorErr    = lipgloss.Color("203") // soft red
	colorOK     = lipgloss.Color("78")  // soft green
	colorSel    = lipgloss.Color("57")  // violet selection

	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(colorSel).Padding(0, 1)
	styleTabInactive = lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)

	styleTitle     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleLabel     = lipgloss.NewStyle().Foreground(colorDim)
	styleCursor    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleLegend    = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	styleStatusOK  = lipgloss.NewStyle().Foreground(colorOK)
	styleStatusErr = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
	styleErrText   = lipgloss.NewStyle().Foreground(colorErr)
	styleHint      = lipgloss.NewStyle().Foreground(colorDim)
)

func newTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colorDim).
		BorderBottom(true).
		Bold(true).
		Foreground(colorText)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("231")).
		Background(colorSel).
		Bold(false)
	return s
}
