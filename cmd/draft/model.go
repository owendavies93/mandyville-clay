package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Draft squad constraints.
const (
	squadGK  = 2
	squadDEF = 5
	squadMID = 5
	squadFWD = 3
	squadMax = 15
)

type posFilter int

const (
	filterAll posFilter = iota
	filterGK
	filterDEF
	filterMID
	filterFWD
)

func (f posFilter) String() string {
	switch f {
	case filterGK:
		return "GK"
	case filterDEF:
		return "DEF"
	case filterMID:
		return "MID"
	case filterFWD:
		return "FWD"
	default:
		return "ALL"
	}
}

func (f posFilter) matches(pos string) bool {
	if f == filterAll {
		return true
	}
	return f.String() == pos
}

type mode int

const (
	modeNormal mode = iota
	modeSearch
	modeSetup
)

type sortMode int

const (
	sortVORP sortMode = iota
	sortH2H
	sortPoints
)

func (s sortMode) String() string {
	switch s {
	case sortH2H:
		return "H2H"
	case sortPoints:
		return "PTS"
	default:
		return "VORP"
	}
}

func (s sortMode) value(p Player) float64 {
	switch s {
	case sortH2H:
		return p.H2HAdjustedPts
	case sortPoints:
		return p.ProjectedPoints
	default:
		return p.VORP
	}
}

type undoEntry struct {
	playerIdx int
	prevState DraftState
	prevPick  int // which pick number it was
}

type model struct {
	data         ProjectionData
	playerState  []DraftState // indexed same as data.Players
	mySquad      []int        // indices into data.Players
	cursor       int
	filter       posFilter
	mode         mode
	searchInput  textinput.Model
	searchQuery  string
	width        int
	height       int
	undoStack    []undoEntry
	pickNumber   int // current overall pick number (1-indexed)
	draftPos     int // my draft position (1-indexed)
	leagueSize   int
	setupInput   textinput.Model
	setupStep    int // 0 = league size, 1 = draft position
	message      string
	sortMode     sortMode
	sessionID    string // set when the session is saved on quit
	quitPending  bool   // q pressed once, awaiting confirmation

	// Cached filtered+sorted view.
	viewPlayers []int // indices into data.Players
}

func newModel(data ProjectionData) model {
	si := textinput.New()
	si.Placeholder = "search..."
	si.CharLimit = 30

	setup := textinput.New()
	setup.Placeholder = "8"
	setup.CharLimit = 2
	setup.Focus()

	m := model{
		data:        data,
		playerState: make([]DraftState, len(data.Players)),
		leagueSize:  data.LeagueSize,
		mode:        modeSetup,
		searchInput: si,
		setupInput:  setup,
		setupStep:   0,
		sortMode:    sortVORP,
	}
	m.rebuildView()
	return m
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.mode == modeSetup {
			return m.updateSetup(msg)
		}
		if m.mode == modeSearch {
			return m.updateSearch(msg)
		}
		return m.updateNormal(msg)
	}

	if m.mode == modeSetup {
		var cmd tea.Cmd
		m.setupInput, cmd = m.setupInput.Update(msg)
		return m, cmd
	}
	if m.mode == modeSearch {
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) updateSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		val := m.setupInput.Value()
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err != nil || n < 1 {
			m.message = "enter a valid number"
			return m, nil
		}
		if m.setupStep == 0 {
			m.leagueSize = n
			m.setupStep = 1
			m.setupInput.SetValue("")
			m.setupInput.Placeholder = fmt.Sprintf("1-%d", n)
			m.message = ""
			return m, nil
		}
		if n < 1 || n > m.leagueSize {
			m.message = fmt.Sprintf("enter 1-%d", m.leagueSize)
			return m, nil
		}
		m.draftPos = n
		m.pickNumber = 1
		m.mode = modeNormal
		m.message = ""
		m.recalcVORP()
		m.rebuildView()
		return m, nil
	}
	var cmd tea.Cmd
	m.setupInput, cmd = m.setupInput.Update(msg)
	return m, cmd
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.saveSession()
		return m, tea.Quit
	case "esc":
		m.mode = modeNormal
		m.searchQuery = ""
		m.searchInput.SetValue("")
		m.rebuildView()
		return m, nil
	case "enter":
		m.searchQuery = m.searchInput.Value()
		m.mode = modeNormal
		m.cursor = 0
		m.rebuildView()
		return m, nil
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	// Live search.
	m.searchQuery = m.searchInput.Value()
	m.cursor = 0
	m.rebuildView()
	return m, cmd
}

func (m model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit handling: ctrl+c quits immediately, q requires a second press.
	switch msg.String() {
	case "ctrl+c":
		m.saveSession()
		return m, tea.Quit
	case "q", "Q":
		if m.quitPending {
			m.saveSession()
			return m, tea.Quit
		}
		m.quitPending = true
		m.message = "press q again to quit"
		return m, nil
	}

	// Any other key cancels a pending quit.
	if m.quitPending {
		m.quitPending = false
		m.message = ""
	}

	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.viewPlayers)-1 {
			m.cursor++
		}
	case "pgup":
		m.cursor -= 20
		if m.cursor < 0 {
			m.cursor = 0
		}
	case "pgdown":
		m.cursor += 20
		if m.cursor >= len(m.viewPlayers) {
			m.cursor = len(m.viewPlayers) - 1
		}

	case "f":
		m.filter = (m.filter + 1) % 5
		m.cursor = 0
		m.rebuildView()

	case "/":
		m.mode = modeSearch
		m.searchInput.Focus()
		return m, textinput.Blink

	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.cursor = 0
			m.rebuildView()
		}

	case "d":
		// Pick the highlighted player for whoever's turn it is:
		// drafts to my squad on my turn, otherwise marks as an
		// opponent's pick.
		if len(m.viewPlayers) == 0 || m.cursor >= len(m.viewPlayers) {
			return m, nil
		}
		idx := m.viewPlayers[m.cursor]
		if m.playerState[idx] != Available {
			return m, nil
		}

		name := fmt.Sprintf("%s %s", m.data.Players[idx].FirstName, m.data.Players[idx].LastName)

		if m.isMyTurn() {
			pos := m.data.Players[idx].Position
			if m.posCount(pos) >= m.posMax(pos) {
				m.message = fmt.Sprintf("squad full at %s", pos)
				return m, nil
			}
			m.undoStack = append(m.undoStack, undoEntry{idx, Available, m.pickNumber})
			m.playerState[idx] = DraftedByMe
			m.mySquad = append(m.mySquad, idx)
			m.pickNumber++
			m.message = "drafted " + name
		} else {
			m.undoStack = append(m.undoStack, undoEntry{idx, Available, m.pickNumber})
			m.playerState[idx] = TakenByOpponent
			m.pickNumber++
			m.message = name + " taken"
		}
		m.recalcVORP()
		m.rebuildView()

	case "u":
		// Undo last action.
		if len(m.undoStack) > 0 {
			entry := m.undoStack[len(m.undoStack)-1]
			m.undoStack = m.undoStack[:len(m.undoStack)-1]
			m.playerState[entry.playerIdx] = entry.prevState
			m.pickNumber = entry.prevPick
			if entry.prevState == Available {
				// Remove from my squad if it was a draft.
				for i, si := range m.mySquad {
					if si == entry.playerIdx {
						m.mySquad = append(m.mySquad[:i], m.mySquad[i+1:]...)
						break
					}
				}
			}
			m.message = fmt.Sprintf("undone: %s %s", m.data.Players[entry.playerIdx].FirstName, m.data.Players[entry.playerIdx].LastName)
			m.recalcVORP()
			m.rebuildView()
		}

	case "s":
		m.sortMode = (m.sortMode + 1) % 3
		m.rebuildView()
	}

	return m, nil
}

func (m *model) rebuildView() {
	m.viewPlayers = nil
	query := strings.ToLower(m.searchQuery)

	for i, p := range m.data.Players {
		if m.playerState[i] != Available {
			continue
		}
		if !m.filter.matches(p.Position) {
			continue
		}
		if query != "" {
			name := strings.ToLower(p.FirstName + " " + p.LastName)
			team := strings.ToLower(p.TeamName)
			if !strings.Contains(name, query) && !strings.Contains(team, query) {
				continue
			}
		}
		m.viewPlayers = append(m.viewPlayers, i)
	}

	// Sort by the active metric, descending.
	sort.Slice(m.viewPlayers, func(a, b int) bool {
		ia, ib := m.viewPlayers[a], m.viewPlayers[b]
		return m.sortMode.value(m.data.Players[ia]) > m.sortMode.value(m.data.Players[ib])
	})

	if m.cursor >= len(m.viewPlayers) {
		m.cursor = len(m.viewPlayers) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *model) recalcVORP() {
	// Recalculate replacement levels based on remaining available players.
	draftedPerPos := map[string]int{
		"GK":  m.leagueSize * squadGK,
		"DEF": m.leagueSize * squadDEF,
		"MID": m.leagueSize * squadMID,
		"FWD": m.leagueSize * squadFWD,
	}

	// Count how many are already taken at each position.
	takenPerPos := map[string]int{}
	for i, state := range m.playerState {
		if state != Available {
			takenPerPos[m.data.Players[i].Position]++
		}
	}

	// Get remaining available players' points by position.
	posPlayers := map[string][]float64{}
	for i, p := range m.data.Players {
		if m.playerState[i] == Available {
			posPlayers[p.Position] = append(posPlayers[p.Position], p.ProjectedPoints)
		}
	}
	for pos := range posPlayers {
		sort.Sort(sort.Reverse(sort.Float64Slice(posPlayers[pos])))
	}

	// Replacement level = the (remaining slots)th best available player.
	replacementLevel := map[string]float64{}
	for pos, total := range draftedPerPos {
		remaining := total - takenPerPos[pos]
		if remaining < 0 {
			remaining = 0
		}
		pts := posPlayers[pos]
		if remaining < len(pts) {
			replacementLevel[pos] = pts[remaining]
		} else if len(pts) > 0 {
			replacementLevel[pos] = pts[len(pts)-1]
		}
	}

	for i := range m.data.Players {
		m.data.Players[i].VORP = m.data.Players[i].ProjectedPoints - replacementLevel[m.data.Players[i].Position]
	}
}

func (m model) posCount(pos string) int {
	count := 0
	for _, idx := range m.mySquad {
		if m.data.Players[idx].Position == pos {
			count++
		}
	}
	return count
}

func (m model) posMax(pos string) int {
	switch pos {
	case "GK":
		return squadGK
	case "DEF":
		return squadDEF
	case "MID":
		return squadMID
	case "FWD":
		return squadFWD
	}
	return 0
}

// currentRoundPick returns which round and pick within the round.
func (m model) currentRoundPick() (int, int, bool) {
	if m.draftPos == 0 {
		return 0, 0, false
	}
	// Snake draft: odd rounds go 1..N, even rounds go N..1.
	pick := m.pickNumber
	round := ((pick - 1) / m.leagueSize) + 1
	posInRound := ((pick - 1) % m.leagueSize) + 1

	var myTurn bool
	if round%2 == 1 {
		// Normal order.
		myTurn = posInRound == m.draftPos
	} else {
		// Reversed.
		myTurn = posInRound == (m.leagueSize - m.draftPos + 1)
	}
	return round, posInRound, myTurn
}

// isMyTurn reports whether the current pick belongs to me.
func (m model) isMyTurn() bool {
	_, _, myTurn := m.currentRoundPick()
	return myTurn
}

// isDraftComplete reports whether every pick in the draft has been made.
func (m model) isDraftComplete() bool {
	return m.pickNumber > m.leagueSize*squadMax
}

// nextMyPick returns the overall pick number for my next pick.
func (m model) nextMyPick() int {
	for pick := m.pickNumber; pick <= m.leagueSize*squadMax; pick++ {
		round := ((pick - 1) / m.leagueSize) + 1
		posInRound := ((pick - 1) % m.leagueSize) + 1
		var isMe bool
		if round%2 == 1 {
			isMe = posInRound == m.draftPos
		} else {
			isMe = posInRound == (m.leagueSize - m.draftPos + 1)
		}
		if isMe {
			return pick
		}
	}
	return 0
}

func (m model) View() string {
	if m.mode == modeSetup {
		return m.viewSetup()
	}
	return m.viewDraft()
}

func (m model) viewSetup() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	b.WriteString(titleStyle.Render("FPL Draft Assistant"))
	b.WriteString("\n\n")

	if m.setupStep == 0 {
		b.WriteString("How many managers in the league?\n\n")
	} else {
		b.WriteString(fmt.Sprintf("League size: %d\n", m.leagueSize))
		b.WriteString("What is your draft position?\n\n")
	}

	b.WriteString(m.setupInput.View())
	b.WriteString("\n")

	if m.message != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		b.WriteString("\n" + errStyle.Render(m.message))
	}

	return b.String()
}

func (m model) viewDraft() string {
	if m.width == 0 {
		return "loading..."
	}

	var sections []string

	// --- Header ---
	sections = append(sections, m.viewHeader())

	// --- Squad ---
	sections = append(sections, m.viewSquad())

	// --- Recommendation ---
	if len(m.viewPlayers) > 0 {
		sections = append(sections, m.viewRecommendation())
	}

	// --- Player list ---
	sections = append(sections, m.viewPlayerList())

	// --- Footer ---
	sections = append(sections, m.viewFooter())

	return strings.Join(sections, "\n")
}

func (m model) viewHeader() string {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Draft is over once every slot has been picked.
	if m.isDraftComplete() {
		doneStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("82"))
		header := fmt.Sprintf("%s  %s",
			headerStyle.Render("Draft"),
			doneStyle.Render("complete — all teams selected"),
		)
		if m.searchQuery != "" {
			searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
			header += "  " + searchStyle.Render(fmt.Sprintf("search: %s", m.searchQuery))
		}
		return header
	}

	round, posInRound, myTurn := m.currentRoundPick()

	var turnStr string
	if myTurn {
		turnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("82"))
		turnStr = turnStyle.Render("YOUR PICK")
	} else if m.myPicksRemaining() == 0 {
		turnStr = dimStyle.Render("squad full")
	} else {
		nextPick := m.nextMyPick()
		picksAway := nextPick - m.pickNumber
		turnStr = dimStyle.Render(fmt.Sprintf("your pick in %d", picksAway))
	}

	header := fmt.Sprintf("%s  %s  %s",
		headerStyle.Render(fmt.Sprintf("Round %d", round)),
		dimStyle.Render(fmt.Sprintf("Pick %d/%d", posInRound, m.leagueSize)),
		turnStr,
	)

	if m.searchQuery != "" {
		searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		header += "  " + searchStyle.Render(fmt.Sprintf("search: %s", m.searchQuery))
	}

	return header
}

func (m model) viewSquad() string {
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	filledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	// One line per position so the whole squad fits on screen at once.
	var b strings.Builder
	for _, pos := range []string{"GK", "DEF", "MID", "FWD"} {
		count := m.posCount(pos)
		max := m.posMax(pos)
		var slots []string
		filled := 0
		for _, idx := range m.mySquad {
			if m.data.Players[idx].Position == pos {
				name := m.data.Players[idx].LastName
				if len(name) > 10 {
					name = name[:10]
				}
				slots = append(slots, filledStyle.Render(name))
				filled++
			}
		}
		for i := filled; i < max; i++ {
			slots = append(slots, dimStyle.Render("_"))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, " %-4s %s (%d/%d)", pos, strings.Join(slots, " "), count, max)
	}

	return b.String()
}

func (m model) viewRecommendation() string {
	recStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))

	// Find the best pick considering positional need.
	bestIdx := -1
	bestScore := math.Inf(-1)

	for _, idx := range m.viewPlayers {
		p := m.data.Players[idx]
		if m.posCount(p.Position) >= m.posMax(p.Position) {
			continue
		}
		score := p.VORP
		// Boost positions we need urgently (approaching draft end).
		slotsLeft := m.posMax(p.Position) - m.posCount(p.Position)
		picksRemaining := m.myPicksRemaining()
		if picksRemaining > 0 && slotsLeft > 0 {
			urgency := float64(slotsLeft) / float64(picksRemaining)
			if urgency > 0.5 {
				score *= 1.2 // boost urgently needed positions
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = idx
		}
	}

	if bestIdx < 0 {
		return ""
	}

	p := m.data.Players[bestIdx]
	return recStyle.Render(fmt.Sprintf("→ %s %s (%s, %s, VORP %.1f)",
		p.FirstName, p.LastName, p.Position, p.TeamName, p.VORP))
}

func (m model) myPicksRemaining() int {
	return squadMax - len(m.mySquad)
}

func (m model) viewPlayerList() string {
	if len(m.viewPlayers) == 0 {
		return "  no players match"
	}

	var b strings.Builder

	// Header row.
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("240"))
	filterLabel := m.filter.String()
	sortLabel := m.sortMode.String()
	b.WriteString(headerStyle.Render(fmt.Sprintf(
		" %-3s %-20s %-4s %-22s %6s %6s %6s",
		"#", "Player", "Pos", "Team", "Pts", sortLabel, "["+filterLabel+"]")))
	b.WriteString("\n")

	// Determine visible range.
	listHeight := m.height - 11 // reserve for header, squad (4 lines), rec, footer
	if listHeight < 5 {
		listHeight = 5
	}

	startIdx := 0
	if m.cursor >= listHeight {
		startIdx = m.cursor - listHeight + 1
	}
	endIdx := startIdx + listHeight
	if endIdx > len(m.viewPlayers) {
		endIdx = len(m.viewPlayers)
	}

	normalStyle := lipgloss.NewStyle()
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	posStyles := map[string]lipgloss.Style{
		"GK":  lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		"DEF": lipgloss.NewStyle().Foreground(lipgloss.Color("82")),
		"MID": lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
		"FWD": lipgloss.NewStyle().Foreground(lipgloss.Color("205")),
	}

	for vi := startIdx; vi < endIdx; vi++ {
		idx := m.viewPlayers[vi]
		p := m.data.Players[idx]

		name := fmt.Sprintf("%s %s", p.FirstName, p.LastName)
		if len(name) > 20 {
			name = name[:20]
		}
		team := p.TeamName
		if len(team) > 22 {
			team = team[:22]
		}

		posStyle := posStyles[p.Position]
		posStr := posStyle.Render(fmt.Sprintf("%-4s", p.Position))

		line := fmt.Sprintf(" %-3d %-20s %s %-22s %6.1f %6.1f",
			vi+1, name, posStr, team, p.ProjectedPoints, m.sortMode.value(p))

		if vi == m.cursor {
			b.WriteString(selectedStyle.Render(line))
		} else {
			b.WriteString(normalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) viewFooter() string {
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	msgStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	var parts []string
	parts = append(parts, "[d]pick")
	parts = append(parts, "[f]ilter")
	parts = append(parts, "[/]search")
	parts = append(parts, "[u]ndo")
	parts = append(parts, "[s]ort")
	parts = append(parts, "[q]uit")

	footer := dimStyle.Render(strings.Join(parts, " · "))

	if m.message != "" {
		footer += "  " + msgStyle.Render(m.message)
	}

	if m.mode == modeSearch {
		footer = m.searchInput.View()
	}

	return footer
}
