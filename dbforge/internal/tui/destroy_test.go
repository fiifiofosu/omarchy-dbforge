package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/model"
)

func testInstance() model.Instance {
	return model.Instance{
		ID: "app-db", Engine: "postgres", Version: "16", Port: 15432,
		DataDir: "/home/u/.local/share/dbforge/postgres/16/app-db",
		Status:  model.StatusRunning, Restart: model.RestartAlways,
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func typeString(m Model, s string) Model {
	for _, r := range s {
		out, _ := m.updateDestroy(key(string(r)))
		m = out.(Model)
	}
	return m
}

// The safe option must be preselected, so mashing enter never deletes data.
func TestDestroyDefaultsToKeepingData(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy

	if m.destroy.choice != destroyContainerOnly {
		t.Fatal("destroy dialog does not default to keeping the data")
	}
}

// Choosing "delete the data" must not act on enter alone.
func TestWipePathRequiresTypingTheName(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy

	// Switch to the destructive option and confirm.
	out, _ := m.updateDestroy(key("down"))
	m = out.(Model)
	if m.destroy.choice != destroyWithData {
		t.Fatal("down did not select the wipe option")
	}

	out, cmd := m.updateDestroy(key("enter"))
	m = out.(Model)
	if cmd != nil {
		t.Fatal("enter on the wipe option issued a command; it must ask for the name first")
	}
	if m.destroy.step != stepTypeName {
		t.Fatalf("step = %v, want stepTypeName", m.destroy.step)
	}
	if m.view != viewConfirmDestroy {
		t.Fatal("the dialog closed without confirmation")
	}
}

func TestWrongNameAbortsTheWipe(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy
	m.destroy.choice = destroyWithData
	m.destroy.step = stepTypeName

	m = typeString(m, "app-d") // one character short
	out, cmd := m.updateDestroy(key("enter"))
	m = out.(Model)

	if cmd != nil {
		t.Fatal("a mismatched name still issued the destroy command")
	}
	if m.destroy.err == nil {
		t.Fatal("no error shown for a mismatched confirmation")
	}
	if m.destroy.typed != "" {
		t.Fatal("the typed text should be cleared after a failed match")
	}
	if m.destroy.step != stepTypeName {
		t.Fatal("a failed confirmation should stay on the confirmation step")
	}
}

func TestCorrectNameProceedsWithTheWipe(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy
	m.destroy.choice = destroyWithData
	m.destroy.step = stepTypeName

	m = typeString(m, "app-db")
	out, cmd := m.updateDestroy(key("enter"))
	m = out.(Model)

	if cmd == nil {
		t.Fatal("the correct name did not trigger the destroy")
	}
	if m.destroy.step != stepWorking {
		t.Fatalf("step = %v, want stepWorking", m.destroy.step)
	}
}

// The keep-data path is the common one and should be a single confirmation.
func TestKeepDataPathConfirmsImmediately(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy

	_, cmd := m.updateDestroy(key("enter"))
	if cmd == nil {
		t.Fatal("the keep-data path should act on enter")
	}
}

func TestEscapeCancelsTheWipeWithoutDestroying(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy
	m.destroy.choice = destroyWithData
	m.destroy.step = stepTypeName
	m = typeString(m, "app-db")

	out, cmd := m.updateDestroy(key("esc"))
	m = out.(Model)
	if cmd != nil {
		t.Fatal("esc issued a destroy command")
	}
	if m.destroy.step != stepChoose {
		t.Fatal("esc from the name prompt should return to the choice")
	}
	if m.destroy.typed != "" {
		t.Fatal("typed text should be cleared when backing out")
	}
}

// The dialog must state where the data lives and that deletion is permanent.
func TestDestroyViewExplainsBothOutcomes(t *testing.T) {
	m := New(nil)
	m.destroy = newDestroyModel(testInstance())
	m.view = viewConfirmDestroy

	out := m.viewDestroy()
	for _, want := range []string{"keep the data", "cannot be undone", testInstance().DataDir} {
		if !strings.Contains(out, want) {
			t.Errorf("destroy view does not mention %q", want)
		}
	}
}
