package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func sendUIMessage(model appModel, message tea.Msg) (appModel, tea.Cmd) {
	updated, command := model.Update(message)
	return updated.(appModel), command
}

func keyMessage(name string) tea.KeyPressMsg {
	switch name {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "escape":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "pageup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pagedown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	default:
		return tea.KeyPressMsg{Code: []rune(name)[0], Text: name}
	}
}

func runUICommand(t *testing.T, model appModel, command tea.Cmd) appModel {
	t.Helper()
	if command == nil {
		t.Fatal("expected an asynchronous command")
	}
	updated, _ := sendUIMessage(model, command())
	return updated
}

func TestTUIQuarantinesOnlyAfterExactPhraseAndRestores(t *testing.T) {
	service, home := testCleaner(t)
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(codex, "config.toml")
	if err := os.WriteFile(file, []byte("private test data"), 0600); err != nil {
		t.Fatal(err)
	}

	model := newModel(service)
	if !strings.Contains(model.View().Content, "Scanning fixed catalog locations...") {
		t.Fatal("startup scan was not shown in the main view")
	}
	command := model.Init()
	model = runUICommand(t, model, command)
	index := -1
	for position, candidate := range model.catalog.Items {
		if candidate.ID == "codex-data" {
			index = position
			break
		}
	}
	if index < 0 {
		t.Fatal("initial scan did not find the Codex fixture")
	}
	model.cursor = index
	model, _ = sendUIMessage(model, keyMessage("space"))
	if len(model.selected) != 1 {
		t.Fatalf("space did not select a target: %v", model.selected)
	}
	model, command = sendUIMessage(model, keyMessage("R"))
	model = runUICommand(t, model, command)
	if model.screen != screenReview || len(model.preview) != 1 {
		t.Fatalf("review state = screen %d, targets %d", model.screen, len(model.preview))
	}
	model, _ = sendUIMessage(model, keyMessage("q"))
	for _, character := range "DELETEX" {
		model, _ = sendUIMessage(model, keyMessage(string(character)))
	}
	model, command = sendUIMessage(model, keyMessage("enter"))
	if command != nil || model.busy {
		t.Fatal("incorrect confirmation started a removal")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("incorrect confirmation changed the fixture: %v", err)
	}
	model, _ = sendUIMessage(model, keyMessage("backspace"))
	model, command = sendUIMessage(model, keyMessage("enter"))
	model = runUICommand(t, model, command)
	if model.screen != screenResults || model.quarantineID == "" {
		t.Fatalf("removal result state = %#v", model)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf("confirmed quarantine did not move the source: %v", err)
	}

	model, command = sendUIMessage(model, keyMessage("enter"))
	model = runUICommand(t, model, command)
	if len(model.catalog.Sessions) != 1 {
		t.Fatalf("scan after quarantine found %d sessions", len(model.catalog.Sessions))
	}
	model, _ = sendUIMessage(model, keyMessage("t"))
	if model.screen != screenRestore {
		t.Fatalf("restore screen was not opened: %d", model.screen)
	}
	model, command = sendUIMessage(model, keyMessage("enter"))
	model = runUICommand(t, model, command)
	if model.screen != screenResults || len(model.results) != 1 || !model.results[0].OK {
		t.Fatalf("restore result state = %#v", model.results)
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "private test data" {
		t.Fatalf("TUI did not restore the file: %q, %v", contents, err)
	}
}

func TestTUIRenderFitsNarrowTerminalAndSanitizesControlText(t *testing.T) {
	model := newModel(nil)
	model.width, model.height = 64, 20
	model.catalog.Items = []item{{
		ID: "unsafe", Name: "A target\x1b[2J with a long label", Group: "remnants", Kind: kindDirectory,
		Targets: []target{{Path: `C:\Users\Test\.codex\` + strings.Repeat("very-long-path-segment\\", 5), Type: "folder"}},
	}}
	model.selected = map[string]struct{}{}
	for _, line := range strings.Split(model.View().Content, "\n") {
		if lipgloss.Width(line) > model.width {
			t.Fatalf("rendered line exceeds terminal width: %d > %d: %q", lipgloss.Width(line), model.width, line)
		}
	}
	if strings.Contains(safeText(model.catalog.Items[0].Name), "\x1b") {
		t.Fatal("control text was not sanitized")
	}
}

func TestTUIRequiresMinimumTerminalSize(t *testing.T) {
	tests := []struct {
		name     string
		screen   viewScreen
		width    int
		height   int
		tooSmall bool
	}{
		{"too narrow", screenRestore, minTerminalWidth - 1, minTerminalHeight + 1, true},
		{"too short", screenMain, minTerminalWidth + 8, minTerminalHeight - 1, true},
		{"minimum size", screenMain, minTerminalWidth, minTerminalHeight, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newModel(nil)
			model.screen, model.width, model.height = test.screen, test.width, test.height
			content := model.View().Content
			if strings.Contains(content, "Too small") != test.tooSmall {
				t.Fatalf("small-terminal message = %q, want present %v", content, test.tooSmall)
			}
			lines := strings.Split(content, "\n")
			if len(lines) > test.height {
				t.Fatalf("rendered height %d exceeds terminal height %d", len(lines), test.height)
			}
			for _, line := range lines {
				if width := lipgloss.Width(line); width > test.width {
					t.Fatalf("rendered width %d exceeds terminal width %d: %q", width, test.width, line)
				}
			}
		})
	}
}
func TestTUIKeepsCurrentTargetVisibleInNarrowList(t *testing.T) {
	model := newModel(nil)
	model.width, model.height = minTerminalWidth, minTerminalHeight
	model.catalog.Items = make([]item, 12)
	for index := range model.catalog.Items {
		model.catalog.Items[index] = item{ID: "target", Name: "Other", Group: "data", Kind: kindFile}
	}
	model.catalog.Items[11].Name = "Cursor target"
	model.cursor = 11

	if content := model.View().Content; !strings.Contains(content, "> [ ] Cursor target") {
		t.Fatalf("cursor target is outside the visible list:\n%s", content)
	}
}

func TestTUIDetailScrollAndAllScreenLayouts(t *testing.T) {
	candidate := item{
		ID: "fixture", Name: "Fixture target", Group: "data", Kind: kindDirectory,
		Targets:     []target{{Path: `C:\Users\Tester\.agent-cleaner\` + strings.Repeat("very-long-path-segment\\", 30), Type: "folder", Bytes: 123}},
		Description: strings.Repeat("Review this detail carefully. ", 30) + "ENDMARK",
		Warning:     "This warning remains visible after scrolling.",
	}
	model := newModel(nil)
	model.width, model.height = minTerminalWidth, minTerminalHeight
	model.screen = screenReview
	model.preview = []item{candidate}
	before := model.View().Content
	after := before
	for page := 0; page < 50 && !strings.Contains(after, "ENDMARK"); page++ {
		model, _ = sendUIMessage(model, keyMessage("pagedown"))
		after = model.View().Content
	}
	if model.detailScroll == 0 || !strings.Contains(after, "ENDMARK") || before == after {
		t.Fatalf("PageDown did not reveal the end of the review details:\n%s", after)
	}

	for _, size := range [][2]int{{minTerminalWidth, minTerminalHeight}, {120, 40}} {
		for _, screen := range []viewScreen{screenMain, screenReview, screenRestore, screenResults} {
			view := newModel(nil)
			view.loading = false
			view.width, view.height = size[0], size[1]
			view.screen = screen
			view.catalog.Items = []item{candidate}
			view.catalog.Sessions = []sessionInfo{{ID: "session-1", CreatedAt: "2026-01-01T00:00:00Z", Count: 1, Names: []string{"Fixture target"}}}
			view.preview = []item{candidate}
			view.resultsTitle = "Removal results"
			view.results = []operationResult{{Name: "Fixture target", OK: false, Detail: "Review the failure details."}}
			for _, line := range strings.Split(view.View().Content, "\n") {
				if width := lipgloss.Width(line); width > size[0] {
					t.Fatalf("screen %d at %dx%d rendered width %d: %q", screen, size[0], size[1], width, line)
				}
			}
			if height := len(strings.Split(view.View().Content, "\n")); height > size[1] {
				t.Fatalf("screen %d at %dx%d rendered height %d", screen, size[0], size[1], height)
			}
		}
	}
}

func TestSummarizeResultsReflectsOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		results []operationResult
		status  string
		tone    statusTone
	}{
		{"empty", nil, "No operations were needed.", toneNeutral},
		{"success", []operationResult{{OK: true}}, "1 operation succeeded.", toneSuccess},
		{"failure", []operationResult{{OK: false}}, "1 operation failed. Review the details.", toneError},
		{"partial", []operationResult{{OK: true}, {OK: false}}, "1 succeeded, 1 failed. Review the failures.", toneWarning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, tone := summarizeResults(test.results)
			if status != test.status || tone != test.tone {
				t.Fatalf("summary = %q, tone %d; want %q, tone %d", status, tone, test.status, test.tone)
			}
		})
	}
}
