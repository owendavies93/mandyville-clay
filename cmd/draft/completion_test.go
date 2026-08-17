package main

import (
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func dKey() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
}

func (m *model) cursorTo(playerIdx int) bool {
	for i, v := range m.viewPlayers {
		if v == playerIdx {
			m.cursor = i
			return true
		}
	}
	return false
}

// TestFinalPickSavesResult checks that the last pick triggers the result save.
func TestFinalPickSavesResult(t *testing.T) {
	const n = 120
	players := make([]Player, n)
	for i := range players {
		players[i] = Player{PlayerID: i + 1, FirstName: "P", LastName: "x", Position: "FWD", TeamName: "T"}
	}
	data := ProjectionData{Season: 2026, LeagueSize: 8, Players: players}

	m := newModel(data)
	m.mode = modeNormal
	m.leagueSize = 8
	m.draftPos = 1
	m.pickNumber = n // last pick (pick 120)

	for i := 0; i < n-1; i++ {
		m.playerState[i] = TakenByOpponent
	}
	m.rebuildView()
	if len(m.viewPlayers) != 1 {
		t.Fatalf("expected 1 viewable player, got %d", len(m.viewPlayers))
	}
	m.cursor = 0

	updated, _ := m.updateNormal(dKey())
	final := updated.(model)

	if final.pickNumber != n+1 {
		t.Fatalf("pickNumber = %d, want %d", final.pickNumber, n+1)
	}
	if !final.isDraftComplete() {
		t.Fatalf("expected draft complete")
	}
	if final.resultPath == "" {
		t.Fatal("resultPath is empty; saveFinalResult did not run")
	}
	t.Cleanup(func() { os.Remove(final.resultPath) })

	if _, err := os.Stat(final.resultPath); err != nil {
		t.Fatalf("result file not written: %v", err)
	}
}

// TestFullDraftSavesResult drives an entire 2-team draft through the normal
// update path and verifies the result file is produced at completion.
func TestFullDraftSavesResult(t *testing.T) {
	// 40 players: 6 GK, 12 DEF, 12 MID, 10 FWD.
	var players []Player
	mk := func(id, start, count int, pos string) {
		for i := 0; i < count; i++ {
			players = append(players, Player{PlayerID: start + i, FirstName: pos, LastName: "p", Position: pos, TeamName: "T"})
		}
	}
	mk(1, 1, 6, "GK")
	mk(7, 7, 12, "DEF")
	mk(19, 19, 12, "MID")
	mk(31, 31, 10, "FWD")

	data := ProjectionData{Season: 2026, LeagueSize: 2, Players: players}
	m := newModel(data)
	m.mode = modeNormal
	m.leagueSize = 2
	m.draftPos = 1
	m.pickNumber = 1
	m.recalcVORP()
	m.rebuildView()

	// Player indices (0-based) for each team's picks, in pick order.
	myTargets := []int{0, 1, 6, 7, 8, 9, 10, 18, 19, 20, 21, 22, 30, 31, 32}      // 2 GK, 5 DEF, 5 MID, 3 FWD
	oppTargets := []int{2, 3, 11, 12, 13, 14, 15, 23, 24, 25, 26, 27, 33, 34, 35} // 2 GK, 5 DEF, 5 MID, 3 FWD

	myIdx, oppIdx := 0, 0
	for pick := 1; pick <= m.leagueSize*squadMax; pick++ {
		var target int
		if m.isMyTurn() {
			target = myTargets[myIdx]
			myIdx++
		} else {
			target = oppTargets[oppIdx]
			oppIdx++
		}
		if !m.cursorTo(target) {
			t.Fatalf("pick %d: player %d not viewable", pick, target)
		}
		updated, _ := m.updateNormal(dKey())
		m = updated.(model)
	}

	if !m.isDraftComplete() {
		t.Fatalf("draft should be complete after %d picks, pickNumber=%d", m.leagueSize*squadMax, m.pickNumber)
	}
	if m.resultPath == "" {
		t.Fatal("resultPath is empty; final result was not saved")
	}
	t.Cleanup(func() { os.Remove(m.resultPath) })

	raw, err := os.ReadFile(m.resultPath)
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("result file is empty")
	}
	if len(m.picks) != m.leagueSize*squadMax {
		t.Fatalf("expected %d picks, got %d", m.leagueSize*squadMax, len(m.picks))
	}

	// Every team must finish with a valid 2/5/5/3 squad.
	for team := 1; team <= m.leagueSize; team++ {
		for _, pos := range []string{"GK", "DEF", "MID", "FWD"} {
			if got := m.teamPosCount(team, pos); got != m.posMax(pos) {
				t.Errorf("team %d %s = %d, want %d", team, pos, got, m.posMax(pos))
			}
		}
	}
}

// TestOpponentPositionLimitRejected ensures an opponent can't exceed a
// positional limit.
func TestOpponentPositionLimitRejected(t *testing.T) {
	players := make([]Player, 6)
	for i := range players {
		players[i] = Player{PlayerID: i + 1, FirstName: "P", LastName: "x", Position: "GK", TeamName: "T"}
	}
	data := ProjectionData{Season: 2026, LeagueSize: 2, Players: players}

	m := newModel(data)
	m.mode = modeNormal
	m.leagueSize = 2
	m.draftPos = 1
	m.pickNumber = 2 // team 2's turn

	// Team 2 already has both its goalkeepers.
	m.picks = []draftPick{
		{PickNumber: 10, Round: 5, Team: 2, PlayerID: 2, Position: "GK"},
		{PickNumber: 11, Round: 6, Team: 2, PlayerID: 3, Position: "GK"},
	}
	m.playerState[1] = TakenByOpponent // player 2
	m.playerState[2] = TakenByOpponent // player 3
	m.rebuildView()
	if !m.cursorTo(3) { // player 4, another GK
		t.Fatal("candidate GK not viewable")
	}

	updated, _ := m.updateNormal(dKey())
	final := updated.(model)

	if final.pickNumber != 2 {
		t.Errorf("pickNumber = %d, want 2 (rejected pick must not advance)", final.pickNumber)
	}
	if len(final.picks) != 2 {
		t.Errorf("picks length = %d, want 2 (no phantom pick)", len(final.picks))
	}
	if final.message != "team 2 full at GK" {
		t.Errorf("message = %q, want %q", final.message, "team 2 full at GK")
	}
}
