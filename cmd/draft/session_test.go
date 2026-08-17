package main

import (
	"os"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	data := ProjectionData{
		Season:     2026,
		LeagueSize: 8,
		Players: []Player{
			{PlayerID: 1, FirstName: "A", LastName: "One", Position: "FWD"},
			{PlayerID: 2, FirstName: "B", LastName: "Two", Position: "MID"},
			{PlayerID: 3, FirstName: "C", LastName: "Three", Position: "GK"},
		},
	}

	m := newModel(data)
	m.leagueSize = 8
	m.draftPos = 3
	m.pickNumber = 7
	m.mySquad = []int{0}
	m.playerState[0] = DraftedByMe
	m.playerState[1] = TakenByOpponent
	m.sortMode = sortH2H
	m.filter = filterMID
	m.picks = []draftPick{{PickNumber: 1, Round: 1, Team: 1, PlayerID: 1, Position: "FWD"}}

	if err := m.saveSession(); err != nil {
		t.Fatalf("saveSession: %v", err)
	}
	id := m.sessionID
	if id == "" {
		t.Fatal("expected non-empty session ID")
	}
	t.Cleanup(func() { os.Remove(sessionPath(id)) })

	s, err := loadSessionByID(id)
	if err != nil {
		t.Fatalf("loadSessionByID: %v", err)
	}

	m2 := newModel(data)
	if err := m2.applySession(s); err != nil {
		t.Fatalf("applySession: %v", err)
	}

	if m2.leagueSize != 8 || m2.draftPos != 3 || m2.pickNumber != 7 {
		t.Errorf("draft metadata mismatch: %+v", m2)
	}
	if m2.sortMode != sortH2H || m2.filter != filterMID {
		t.Errorf("view state mismatch: sort=%v filter=%v", m2.sortMode, m2.filter)
	}
	if len(m2.mySquad) != 1 || m2.mySquad[0] != 0 {
		t.Errorf("my squad mismatch: %v", m2.mySquad)
	}
	if m2.playerState[0] != DraftedByMe || m2.playerState[1] != TakenByOpponent {
		t.Errorf("player state mismatch: %v", m2.playerState)
	}
	if len(m2.picks) != 1 || m2.picks[0].PlayerID != 1 {
		t.Errorf("picks mismatch: %v", m2.picks)
	}
}

func TestApplySessionSeasonMismatch(t *testing.T) {
	data := ProjectionData{Season: 2026}
	m := newModel(data)
	err := m.applySession(&Session{Season: 2025})
	if err == nil {
		t.Fatal("expected season mismatch error")
	}
}
