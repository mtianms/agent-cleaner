package main

import (
	"fmt"
	"image/color"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type viewScreen uint8

const (
	screenMain viewScreen = iota
	screenReview
	screenRestore
	screenResults
)

const (
	minTerminalWidth  = 25
	minTerminalHeight = 20
)

type statusTone uint8

const (
	toneNeutral statusTone = iota
	toneSuccess
	toneWarning
	toneError
)

var (
	colorAmber  = lipgloss.Color("#e5a637")
	colorRed    = lipgloss.Color("#df776d")
	colorGreen  = lipgloss.Color("#91b989")
	colorMuted  = lipgloss.Color("#c3bcb1")
	colorBorder = lipgloss.Color("#514a3c")

	styleBrand  = lipgloss.NewStyle().Bold(true).Foreground(colorAmber)
	styleAccent = lipgloss.NewStyle().Bold(true).Foreground(colorAmber)
	styleDanger = lipgloss.NewStyle().Bold(true).Foreground(colorRed)
	styleGood   = lipgloss.NewStyle().Bold(true).Foreground(colorGreen)
	styleMuted  = lipgloss.NewStyle().Foreground(colorMuted)
	styleFocus  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#171a1c")).Background(colorAmber).Padding(0, 1)
)

type appModel struct {
	service *cleaner
	screen  viewScreen

	catalog     catalog
	loading     bool
	busy        bool
	cursor      int
	selected    map[string]struct{}
	actionFocus int

	preview       []item
	previewCursor int
	detailScroll  int
	mode          removalMode
	confirming    bool
	confirmation  string

	sessionCursor int
	resultsTitle  string
	results       []operationResult
	resultCursor  int
	quarantineID  string

	status     string
	statusTone statusTone
	width      int
	height     int
}

type scanFinishedMsg struct{ catalog catalog }
type previewFinishedMsg struct {
	items []item
	err   error
}
type removalFinishedMsg struct {
	outcome removalOutcome
	err     error
}
type restoreFinishedMsg struct{ results []operationResult }

func newModel(service *cleaner) appModel {
	return appModel{
		service:     service,
		loading:     true,
		selected:    make(map[string]struct{}),
		actionFocus: -1,
		status:      "Starting scan of known locations...",
		statusTone:  toneWarning,
		width:       80,
		height:      24,
	}
}

func (m appModel) Init() tea.Cmd { return m.scanCmd() }

func (m appModel) scanCmd() tea.Cmd {
	service := m.service
	return func() tea.Msg { return scanFinishedMsg{catalog: service.scan()} }
}

func (m appModel) previewCmd() tea.Cmd {
	ids := make([]string, 0, len(m.selected))
	for _, candidate := range m.catalog.Items {
		if _, ok := m.selected[candidate.ID]; ok {
			ids = append(ids, candidate.ID)
		}
	}
	service := m.service
	return func() tea.Msg {
		items, err := service.preview(ids)
		return previewFinishedMsg{items: items, err: err}
	}
}

func (m appModel) removeCmd() tea.Cmd {
	ids := make([]string, 0, len(m.selected))
	for _, candidate := range m.catalog.Items {
		if _, ok := m.selected[candidate.ID]; ok {
			ids = append(ids, candidate.ID)
		}
	}
	service, mode, confirmation := m.service, m.mode, m.confirmation
	return func() tea.Msg {
		outcome, err := service.remove(ids, mode, confirmation)
		return removalFinishedMsg{outcome: outcome, err: err}
	}
}

func (m appModel) restoreCmd() tea.Cmd {
	sessionID := m.catalog.Sessions[m.sessionCursor].ID
	service := m.service
	return func() tea.Msg { return restoreFinishedMsg{results: service.restore(sessionID)} }
}

func (m appModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case scanFinishedMsg:
		m.catalog = message.catalog
		m.loading = false
		m.selected = make(map[string]struct{})
		m.actionFocus = -1
		if m.cursor >= len(m.catalog.Items) {
			m.cursor = maxInt(0, len(m.catalog.Items)-1)
		}
		m.detailScroll = 0
		m.setStatus("Scan complete.", toneSuccess)
	case previewFinishedMsg:
		m.busy = false
		if message.err != nil {
			m.setStatus("Preview failed: "+safeError(message.err), toneError)
			return m, nil
		}
		m.preview = message.items
		m.previewCursor = 0
		m.detailScroll = 0
		m.mode = ""
		m.confirming = false
		m.confirmation = ""
		m.screen = screenReview
		m.setStatus("Review every path and warning before choosing an action.", toneWarning)
	case removalFinishedMsg:
		m.busy = false
		m.screen = screenResults
		m.resultsTitle = "Removal results"
		m.results = message.outcome.Results
		m.resultCursor = 0
		m.detailScroll = 0
		m.quarantineID = message.outcome.QuarantineID
		if message.err != nil {
			m.results = []operationResult{{OK: false, Name: "Removal failed", Detail: safeError(message.err)}}
			m.setStatus("Removal failed. Review the error and rescan.", toneError)
		} else {
			status, tone := summarizeResults(m.results)
			m.setStatus(status, tone)
		}
	case restoreFinishedMsg:
		m.busy = false
		m.screen = screenResults
		m.resultsTitle = "Restore results"
		m.results = message.results
		m.resultCursor = 0
		m.detailScroll = 0
		m.quarantineID = ""
		status, tone := summarizeResults(m.results)
		m.setStatus(status, tone)
	case tea.PasteMsg:
		if m.screen == screenReview && m.confirming {
			m.confirmation += safeText(message.Content)
		}
	case tea.KeyPressMsg:
		return m.updateKey(message)
	}
	return m, nil
}

func (m appModel) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyName := strings.ToLower(key.String())
	if keyName == "ctrl+c" || keyName == "ctrl+q" {
		if m.busy {
			m.setStatus("Wait for the current operation to finish before quitting.", toneWarning)
			return m, nil
		}
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if !(m.screen == screenReview && m.confirming) {
		switch key.Key().Code {
		case tea.KeyPgUp:
			m.detailScroll = maxInt(0, m.detailScroll-maxInt(1, m.height/2))
			return m, nil
		case tea.KeyPgDown:
			m.detailScroll += maxInt(1, m.height/2)
			return m, nil
		}
	}
	switch m.screen {
	case screenMain:
		return m.updateMainKey(keyName)
	case screenReview:
		return m.updateReviewKey(key)
	case screenRestore:
		return m.updateRestoreKey(keyName)
	case screenResults:
		return m.updateResultsKey(keyName)
	}
	return m, nil
}

func (m appModel) updateMainKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.detailScroll = 0
		}
		m.actionFocus = -1
	case "down", "j":
		if m.cursor+1 < len(m.catalog.Items) {
			m.cursor++
			m.detailScroll = 0
		}
		m.actionFocus = -1
	case "space", "enter":
		if m.actionFocus >= 0 {
			return m.activateAction()
		}
		m.toggleCurrent()
	case "tab":
		m.actionFocus = (m.actionFocus + 1) % 4
	case "shift+tab":
		if m.actionFocus < 0 {
			m.actionFocus = 3
		} else {
			m.actionFocus = (m.actionFocus + 3) % 4
		}
	case "r":
		return m.beginReview()
	case "t":
		return m.beginRestore()
	case "s":
		return m.beginScan()
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m appModel) activateAction() (tea.Model, tea.Cmd) {
	switch m.actionFocus {
	case 0:
		return m.beginReview()
	case 1:
		return m.beginRestore()
	case 2:
		return m.beginScan()
	case 3:
		return m, tea.Quit
	}
	return m, nil
}

func (m appModel) toggleCurrent() {
	if len(m.catalog.Items) == 0 || m.cursor < 0 || m.cursor >= len(m.catalog.Items) {
		return
	}
	id := m.catalog.Items[m.cursor].ID
	if _, ok := m.selected[id]; ok {
		delete(m.selected, id)
	} else {
		m.selected[id] = struct{}{}
	}
}

func (m appModel) beginScan() (tea.Model, tea.Cmd) {
	m.loading = true
	m.setStatus("Scanning known locations...", toneWarning)
	return m, m.scanCmd()
}

func (m appModel) beginReview() (tea.Model, tea.Cmd) {
	if len(m.selected) == 0 {
		m.setStatus("Select at least one detected target first.", toneWarning)
		return m, nil
	}
	m.busy = true
	m.detailScroll = 0
	m.setStatus("Refreshing selected targets for review...", toneWarning)
	return m, m.previewCmd()
}

func (m appModel) beginRestore() (tea.Model, tea.Cmd) {
	if len(m.catalog.Sessions) == 0 {
		m.setStatus("No recoverable quarantine sessions were found.", toneWarning)
		return m, nil
	}
	m.sessionCursor = 0
	m.detailScroll = 0
	m.screen = screenRestore
	m.setStatus("Choose a quarantine session to restore.", toneNeutral)
	return m, nil
}

func (m appModel) updateReviewKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyName := strings.ToLower(key.String())
	if keyName == "esc" || keyName == "escape" {
		m.screen = screenMain
		m.mode = ""
		m.confirming = false
		m.confirmation = ""
		m.detailScroll = 0
		m.setStatus("Removal cancelled.", toneNeutral)
		return m, nil
	}
	if m.confirming {
		switch keyName {
		case "backspace", "backspace2", "ctrl+h":
			confirmation := []rune(m.confirmation)
			if len(confirmation) > 0 {
				m.confirmation = string(confirmation[:len(confirmation)-1])
			}
		case "ctrl+u":
			m.confirmation = ""
		case "enter":
			if m.confirmation != expectedConfirmation(m.mode) {
				m.setStatus("The phrase must match exactly before continuing.", toneError)
				return m, nil
			}
			m.busy = true
			m.setStatus("Applying the confirmed removal...", toneWarning)
			return m, m.removeCmd()
		default:
			m.confirmation += key.Key().Text
		}
		return m, nil
	}
	switch keyName {
	case "up", "k":
		if m.previewCursor > 0 {
			m.previewCursor--
			m.detailScroll = 0
		}
	case "down", "j":
		if m.previewCursor+1 < len(m.preview) {
			m.previewCursor++
			m.detailScroll = 0
		}
	case "q":
		if hasFileTargets(m.preview) {
			m.mode = modeQuarantine
			m.confirming = true
			m.confirmation = ""
			m.setStatus("Type DELETE exactly to move files into quarantine.", toneWarning)
		} else {
			m.setStatus("Package removal is always permanent; choose permanent deletion.", toneWarning)
		}
	case "p":
		m.mode = modePermanent
		m.confirming = true
		m.confirmation = ""
		m.setStatus("Type DELETE PERMANENTLY exactly to continue.", toneError)
	}
	return m, nil
}

func (m appModel) updateRestoreKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "escape":
		m.screen = screenMain
		m.detailScroll = 0
		m.setStatus("Restore cancelled.", toneNeutral)
	case "up", "k":
		if m.sessionCursor > 0 {
			m.sessionCursor--
			m.detailScroll = 0
		}
	case "down", "j":
		if m.sessionCursor+1 < len(m.catalog.Sessions) {
			m.sessionCursor++
			m.detailScroll = 0
		}
	case "enter":
		if m.sessionCursor >= 0 && m.sessionCursor < len(m.catalog.Sessions) {
			m.busy = true
			m.setStatus("Restoring selected quarantine session...", toneWarning)
			return m, m.restoreCmd()
		}
	}
	return m, nil
}

func (m appModel) updateResultsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.resultCursor > 0 {
			m.resultCursor--
			m.detailScroll = 0
		}
	case "down", "j":
		if m.resultCursor+1 < len(m.results) {
			m.resultCursor++
			m.detailScroll = 0
		}
	case "enter", "esc", "escape", "q":
		m.screen = screenMain
		m.results = nil
		m.quarantineID = ""
		m.loading = true
		m.detailScroll = 0
		m.setStatus("Refreshing the catalog after the operation...", toneWarning)
		return m, m.scanCmd()
	}
	return m, nil
}

func (m appModel) View() tea.View {
	var view tea.View
	view.AltScreen = true
	view.WindowTitle = "Agent Cleaner"
	view.BackgroundColor = color.RGBA{R: 23, G: 26, B: 28, A: 255}
	view.ForegroundColor = color.RGBA{R: 232, G: 227, B: 218, A: 255}
	if m.width < minTerminalWidth || m.height < minTerminalHeight {
		message := fmt.Sprintf("Too small: need %dx%d.", minTerminalWidth, minTerminalHeight)
		view.SetContent(cutLine(message, maxInt(0, m.width)))
		return view
	}
	var content string
	switch m.screen {
	case screenMain:
		content = m.renderMain()
	case screenReview:
		content = m.renderReview()
	case screenRestore:
		content = m.renderRestore()
	case screenResults:
		content = m.renderResults()
	}
	view.SetContent(content)
	return view
}

func (m appModel) renderMain() string {
	width, height := maxInt(12, m.width), maxInt(12, m.height)
	help := "Up/Down move · Space select · PgUp/PgDn details · R review · T restore · S rescan · Q quit"
	if width < 45 {
		help = "Arrows move · Space select · PgUp/PgDn scroll\nR review · T restore · S scan · Q quit"
	} else if width >= 55 {
		help += " · Tab focuses actions · Enter selects or activates focus"
	}
	helpLines := make([]string, 0, 4)
	for _, line := range strings.Split(help, "\n") {
		helpLines = append(helpLines, wrapLine(line, width)...)
	}
	bodyHeight := maxInt(6, height-6-len(helpLines))
	listHeight := bodyHeight
	if width < 104 {
		listHeight = maxInt(6, bodyHeight/2)
	}
	header := styleBrand.Render("Agent Cleaner")
	if width >= 45 {
		header += "  " + styleMuted.Render("Windows cleanup")
	}
	summary := fmt.Sprintf("%d targets · %d quarantine sessions", len(m.catalog.Items), len(m.catalog.Sessions))
	if width >= 45 {
		summary = fmt.Sprintf("%d targets detected   %d quarantine sessions", len(m.catalog.Items), len(m.catalog.Sessions))
	}
	if m.loading {
		summary = "Scanning known locations..."
	}

	listLines := make([]string, 0, len(m.catalog.Items))
	if m.loading && len(m.catalog.Items) == 0 {
		listLines = append(listLines, "Scanning fixed catalog locations...")
	} else if len(m.catalog.Items) == 0 {
		listLines = append(listLines, "No supported targets found in the catalog locations.")
	} else {
		visible := maxInt(1, listHeight-4)
		start := visibleStart(m.cursor, len(m.catalog.Items), visible)
		end := minInt(len(m.catalog.Items), start+visible)
		for index := start; index < end; index++ {
			candidate := m.catalog.Items[index]
			check := "[ ]"
			if _, ok := m.selected[candidate.ID]; ok {
				check = "[x]"
			}
			marker := "  "
			if index == m.cursor {
				marker = "> "
			}
			label := fmt.Sprintf("%s %s", check, safeText(candidate.Name))
			if width >= 45 {
				meta := kindLabel(candidate.Kind)
				if candidate.Version != "" {
					meta += "  " + candidate.Version
				}
				label += "  " + safeText(meta)
			}
			listLines = append(listLines, cutLine(marker+label, maxInt(1, width-4)))
		}
		if start > 0 || end < len(m.catalog.Items) {
			listLines = append(listLines, fmt.Sprintf("Showing %d-%d of %d", start+1, end, len(m.catalog.Items)))
		}
	}

	detailLines := []string{"Select a target to see its path, size, and warnings."}
	if m.cursor >= 0 && m.cursor < len(m.catalog.Items) {
		detailLines = itemDetails(m.catalog.Items[m.cursor])
	}
	if m.loading && len(m.catalog.Items) == 0 {
		detailLines = []string{"Scanning known locations...", "The catalog checks fixed paths and global package roots."}
	}

	var panels string
	if width >= 104 {
		leftWidth := (width - 1) * 42 / 100
		if leftWidth < 36 {
			leftWidth = 36
		}
		rightWidth := width - leftWidth - 1
		left := renderPanel("Targets", listLines, leftWidth, bodyHeight, colorAmber, 0)
		right := renderPanel("Target details", detailLines, rightWidth, bodyHeight, colorBorder, m.detailScroll)
		panels = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	} else {
		detailHeight := maxInt(4, bodyHeight-listHeight)
		panels = lipgloss.JoinVertical(lipgloss.Left,
			renderPanel("Targets", listLines, width, listHeight, colorAmber, 0),
			renderPanel("Target details", detailLines, width, detailHeight, colorBorder, m.detailScroll),
		)
	}

	selection := fmt.Sprintf("Selected: %d", len(m.selected))
	return strings.Join([]string{
		header,
		cutLine(summary, width),
		panels,
		styleAccent.Render(cutLine(selection, width)),
		m.renderActions(width),
		m.renderStatus(width),
		styleMuted.Render(strings.Join(helpLines, "\n")),
	}, "\n")
}

func (m appModel) renderActions(width int) string {
	labels := []string{"Review", "Restore", "Rescan", "Quit"}
	if width < 55 {
		labels = []string{"R", "T", "S", "Q"}
	}
	parts := make([]string, 0, len(labels))
	for index, label := range labels {
		if index == m.actionFocus {
			parts = append(parts, styleFocus.Render(label))
		} else {
			parts = append(parts, styleMuted.Render("["+label+"]"))
		}
	}
	return strings.Join(parts, "  ")
}

func (m appModel) renderReview() string {
	width, height := maxInt(12, m.width), maxInt(12, m.height)
	if m.previewCursor < 0 || m.previewCursor >= len(m.preview) {
		m.previewCursor = 0
	}
	details := []string{"No target selected."}
	if len(m.preview) > 0 {
		details = itemDetails(m.preview[m.previewCursor])
	}
	if hasPackages(m.preview) {
		details = append(details, "", "Package targets are always uninstalled permanently.")
	}
	if m.confirming {
		details = append(details, "", "Confirmation phrase: "+expectedConfirmation(m.mode), "Input: "+m.confirmation)
	} else if len(m.preview) == 0 {
		details = append(details, "", "No selected targets remain.")
	} else if hasFileTargets(m.preview) {
		details = append(details, "", "Q: move file targets to quarantine.", "P: permanently remove selected targets.")
	} else {
		details = append(details, "", "P: permanently uninstall selected packages.")
	}

	heading := "Review selected targets"
	if len(m.preview) > 0 {
		heading = fmt.Sprintf("Review target %d of %d", m.previewCursor+1, len(m.preview))
	}
	footer := "Esc back"
	if len(m.preview) > 0 {
		footer = "Up/Down target · PgUp/PgDn details · Esc back"
		if hasFileTargets(m.preview) {
			footer = "Up/Down target · PgUp/PgDn details · Q quarantine · P permanent · Esc back"
		} else {
			footer = "Up/Down target · PgUp/PgDn details · P permanently remove packages · Esc back"
		}
	}
	if m.confirming {
		footer = "Type the exact phrase · Enter confirms · Backspace edits · Esc cancels"
	}
	footerLines := wrapLine(footer, width)
	panelHeight := maxInt(4, height-2-len(footerLines))
	panel := renderPanel("Target details", details, width, panelHeight, colorBorder, m.detailScroll)
	return strings.Join([]string{
		styleBrand.Render(cutLine(heading, width)),
		panel,
		m.renderStatus(width),
		styleMuted.Render(strings.Join(footerLines, "\n")),
	}, "\n")
}

func (m appModel) renderRestore() string {
	width, height := maxInt(12, m.width), maxInt(12, m.height)
	if m.sessionCursor < 0 || m.sessionCursor >= len(m.catalog.Sessions) {
		m.sessionCursor = 0
	}
	heading := "Restore quarantine"
	details := []string{"No recoverable quarantine sessions were found."}
	if len(m.catalog.Sessions) > 0 {
		session := m.catalog.Sessions[m.sessionCursor]
		heading = fmt.Sprintf("Restore session %d of %d", m.sessionCursor+1, len(m.catalog.Sessions))
		details = []string{
			"Session: " + safeText(session.ID),
			"Created: " + safeText(session.CreatedAt),
			fmt.Sprintf("Pending items: %d", session.Count),
			"",
			"Items:",
		}
		for _, name := range session.Names {
			details = append(details, "  "+safeText(name))
		}
		details = append(details, "", "Restore never overwrites an existing source path.")
	}
	footerLines := wrapLine("Up/Down session · PgUp/PgDn details · Enter restores this session · Esc back", width)
	panelHeight := maxInt(4, height-2-len(footerLines))
	panel := renderPanel("Session contents", details, width, panelHeight, colorBorder, m.detailScroll)
	return strings.Join([]string{
		styleBrand.Render(cutLine(heading, width)),
		panel,
		m.renderStatus(width),
		styleMuted.Render(strings.Join(footerLines, "\n")),
	}, "\n")
}

func (m appModel) renderResults() string {
	width, height := maxInt(12, m.width), maxInt(12, m.height)
	lines := []string{"No operations were needed."}
	if len(m.results) > 0 {
		if m.resultCursor < 0 || m.resultCursor >= len(m.results) {
			m.resultCursor = 0
		}
		result := m.results[m.resultCursor]
		state := "FAILED"
		if result.OK {
			state = "OK"
		}
		name := firstNonEmpty(result.Name, result.Message)
		lines = []string{
			fmt.Sprintf("%s  %s  (%d of %d)", state, safeText(name), m.resultCursor+1, len(m.results)),
			"",
		}
		if result.Detail != "" {
			lines = append(lines, strings.Split(safeText(result.Detail), "\n")...)
		}
		if result.Message != "" && result.Name != "" {
			lines = append(lines, safeText(result.Message))
		}
	}
	if m.quarantineID != "" {
		lines = append(lines, "", "Restore session: "+m.quarantineID)
	}
	heading := "Operation results"
	if len(m.results) > 0 {
		heading = fmt.Sprintf("Result %d of %d", m.resultCursor+1, len(m.results))
	}
	footerLines := wrapLine("Up/Down result · PgUp/PgDn details · Enter returns and rescans · Esc returns", width)
	panelHeight := maxInt(6, height-2-len(footerLines))
	panel := renderPanel(m.resultsTitle, lines, width, panelHeight, colorAmber, m.detailScroll)
	return strings.Join([]string{
		styleBrand.Render(cutLine(heading, width)),
		panel,
		m.renderStatus(width),
		styleMuted.Render(strings.Join(footerLines, "\n")),
	}, "\n")
}
func summarizeResults(results []operationResult) (string, statusTone) {
	if len(results) == 0 {
		return "No operations were needed.", toneNeutral
	}
	succeeded := 0
	for _, result := range results {
		if result.OK {
			succeeded++
		}
	}
	failed := len(results) - succeeded
	if failed == 0 {
		if succeeded == 1 {
			return "1 operation succeeded.", toneSuccess
		}
		return fmt.Sprintf("%d operations succeeded.", succeeded), toneSuccess
	}
	if succeeded == 0 {
		if failed == 1 {
			return "1 operation failed. Review the details.", toneError
		}
		return fmt.Sprintf("%d operations failed. Review the details.", failed), toneError
	}
	return fmt.Sprintf("%d succeeded, %d failed. Review the failures.", succeeded, failed), toneWarning
}

func (m appModel) renderStatus(width int) string {
	status := cutLine(safeText(m.status), width)
	switch m.statusTone {
	case toneSuccess:
		return styleGood.Render(status)
	case toneWarning:
		return styleAccent.Render(status)
	case toneError:
		return styleDanger.Render(status)
	default:
		return styleMuted.Render(status)
	}
}

func itemDetails(candidate item) []string {
	lines := []string{
		safeText(candidate.Name),
		"Category: " + strings.ToUpper(safeText(candidate.Group)),
		"Type: " + kindLabel(candidate.Kind),
	}
	if candidate.Version != "" {
		lines = append(lines, "Version: "+safeText(candidate.Version))
	}
	for _, target := range candidate.Targets {
		lines = append(lines, "", "Path: "+safeText(target.Path))
		if candidate.Kind == kindNPM || candidate.Kind == kindBun {
			manager := "npm"
			if candidate.Kind == kindBun {
				manager = "Bun"
			}
			lines = append(lines, "Action: uninstall via "+manager)
		} else {
			lines = append(lines, "Size: "+formatBytes(target))
		}
	}
	if candidate.Description != "" {
		lines = append(lines, "", safeText(candidate.Description))
	}
	if candidate.Warning != "" {
		lines = append(lines, "", "WARNING: "+safeText(candidate.Warning))
	}
	return lines
}

func renderPanel(title string, lines []string, width, height int, borderColor color.Color, scroll int) string {
	width, height = maxInt(8, width), maxInt(4, height)
	innerWidth := maxInt(1, width-4)
	maxLines := maxInt(1, height-3)
	var body []string
	if scroll > 0 {
		body = wrapLinesAt(lines, innerWidth, maxLines, scroll)
	} else {
		body = wrapLines(lines, innerWidth, maxLines)
	}
	content := styleAccent.Render(cutLine(safeText(title), innerWidth))
	if len(body) > 0 {
		content += "\n" + strings.Join(body, "\n")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(width).
		Render(content)
}

func wrapLines(lines []string, width, limit int) []string {
	wrapped := make([]string, 0, minInt(limit, len(lines)))
	for _, line := range lines {
		for _, part := range wrapLine(safeText(line), width) {
			wrapped = append(wrapped, part)
			if len(wrapped) == limit {
				return wrapped
			}
		}
	}
	return wrapped
}
func wrapLinesAt(lines []string, width, limit, offset int) []string {
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, wrapLine(safeText(line), width)...)
	}
	start := minInt(maxInt(0, offset), maxInt(0, len(wrapped)-limit))
	return wrapped[start:minInt(len(wrapped), start+limit)]
}

func wrapLine(line string, width int) []string {
	width = maxInt(1, width)
	if line == "" {
		return []string{""}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, 1)
	current := ""
	for _, word := range words {
		if lipgloss.Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			chunks := splitWidth(word, width)
			if len(chunks) > 1 {
				lines = append(lines, chunks[:len(chunks)-1]...)
			}
			current = chunks[len(chunks)-1]
			continue
		}
		if current == "" {
			current = word
		} else if lipgloss.Width(current)+1+lipgloss.Width(word) <= width {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitWidth(value string, width int) []string {
	parts := make([]string, 0, lipgloss.Width(value)/width+1)
	current := ""
	for _, character := range value {
		text := string(character)
		if current != "" && lipgloss.Width(current)+lipgloss.Width(text) > width {
			parts = append(parts, current)
			current = ""
		}
		current += text
	}
	if current != "" {
		parts = append(parts, current)
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func cutLine(value string, width int) string {
	value = safeText(value)
	width = maxInt(0, width)
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 0 {
		return ""
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	limit := width - 3
	var result strings.Builder
	usedWidth := 0
	for _, character := range value {
		partWidth := lipgloss.Width(string(character))
		if usedWidth+partWidth > limit {
			break
		}
		result.WriteRune(character)
		usedWidth += partWidth
	}
	return result.String() + "..."
}

func safeText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' {
			return character
		}
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
}

func formatBytes(target target) string {
	value := float64(target.Bytes)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	size := fmt.Sprintf("%.0f", value)
	if unit > 0 && value < 10 {
		size = fmt.Sprintf("%.1f", value)
	}
	prefix := ""
	if target.SizeApproximate {
		prefix = "at least "
	}
	return prefix + size + " " + units[unit]
}

func kindLabel(kind itemKind) string {
	switch kind {
	case kindNPM:
		return "npm package"
	case kindBun:
		return "Bun package"
	case kindFile:
		return "file"
	case kindDirectory:
		return "folder"
	default:
		return string(kind)
	}
}

func expectedConfirmation(mode removalMode) string {
	if mode == modeQuarantine {
		return "DELETE"
	}
	return "DELETE PERMANENTLY"
}

func hasPackages(items []item) bool {
	for _, candidate := range items {
		if candidate.Kind == kindNPM || candidate.Kind == kindBun {
			return true
		}
	}
	return false
}

func (m *appModel) setStatus(message string, tone statusTone) {
	m.status, m.statusTone = message, tone
}

func visibleStart(cursor, count, visible int) int {
	if count <= visible {
		return 0
	}
	start := cursor - visible + 1
	if start < 0 {
		return 0
	}
	return minInt(start, count-visible)
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}

func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}
