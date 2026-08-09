package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// textField is a minimal single-line text input. It replaces
// bubbles/textinput, whose transitive dependency (atotto/clipboard) is not
// pinned in this module.
type textField struct {
	Prompt      string
	Placeholder string
	CharLimit   int
	// Masked renders the value as asterisks (for passwords).
	Masked bool

	value   []rune
	pos     int
	focused bool
}

var (
	styleFieldCursor      = lipgloss.NewStyle().Reverse(true)
	styleFieldPlaceholder = lipgloss.NewStyle().Foreground(colorDim)
)

func newTextField() textField {
	return textField{Prompt: "> ", CharLimit: 256}
}

func (t *textField) Focus()       { t.focused = true }
func (t *textField) Blur()        { t.focused = false }
func (t textField) Focused() bool { return t.focused }
func (t textField) Value() string { return string(t.value) }
func (t *textField) SetValue(s string) {
	t.value = []rune(s)
	t.pos = len(t.value)
}

func (t textField) Update(msg tea.Msg) (textField, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok || !t.focused {
		return t, nil
	}
	switch km.Type {
	case tea.KeyRunes, tea.KeySpace:
		runes := km.Runes
		if km.Type == tea.KeySpace && len(runes) == 0 {
			runes = []rune{' '}
		}
		if km.Alt {
			return t, nil
		}
		for _, r := range runes {
			if t.CharLimit > 0 && len(t.value) >= t.CharLimit {
				break
			}
			t.value = append(t.value[:t.pos], append([]rune{r}, t.value[t.pos:]...)...)
			t.pos++
		}
	case tea.KeyBackspace:
		if t.pos > 0 {
			t.value = append(t.value[:t.pos-1], t.value[t.pos:]...)
			t.pos--
		}
	case tea.KeyDelete:
		if t.pos < len(t.value) {
			t.value = append(t.value[:t.pos], t.value[t.pos+1:]...)
		}
	case tea.KeyLeft:
		if t.pos > 0 {
			t.pos--
		}
	case tea.KeyRight:
		if t.pos < len(t.value) {
			t.pos++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		t.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		t.pos = len(t.value)
	case tea.KeyCtrlU:
		t.value = nil
		t.pos = 0
	}
	return t, nil
}

func (t textField) View() string {
	shown := string(t.value)
	if t.Masked {
		shown = strings.Repeat("*", len(t.value))
	}
	if len(t.value) == 0 && !t.focused && t.Placeholder != "" {
		return t.Prompt + styleFieldPlaceholder.Render(t.Placeholder)
	}
	if !t.focused {
		return t.Prompt + shown
	}
	r := []rune(shown)
	switch {
	case t.pos >= len(r):
		return t.Prompt + shown + styleFieldCursor.Render(" ")
	default:
		return t.Prompt + string(r[:t.pos]) + styleFieldCursor.Render(string(r[t.pos])) + string(r[t.pos+1:])
	}
}
