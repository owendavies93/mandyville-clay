package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTeamAtPick(t *testing.T) {
	m := model{leagueSize: 8, draftPos: 3}
	cases := []struct {
		pick int
		team int
	}{
		{1, 1}, {3, 3}, {8, 8},
		{9, 8}, {10, 7}, {16, 1},
		{17, 1},
	}
	for _, c := range cases {
		if got := m.teamAtPick(c.pick); got != c.team {
			t.Errorf("teamAtPick(%d) = %d, want %d", c.pick, got, c.team)
		}
	}
}

func TestSaveFinalResult(t *testing.T) {
	data := ProjectionData{
		Season:     2026,
		LeagueSize: 8,
		Players: []Player{
			{PlayerID: 1, FirstName: "A", LastName: "One", Position: "FWD", TeamName: "T1"},
			{PlayerID: 2, FirstName: "B", LastName: "Two", Position: "MID", TeamName: "T2"},
			{PlayerID: 3, FirstName: "C", LastName: "Three", Position: "GK", TeamName: "T3"},
		},
	}
	m := newModel(data)
	m.leagueSize = 8
	m.draftPos = 3
	m.picks = []draftPick{
		{PickNumber: 1, Round: 1, Team: 1, PlayerID: 1, Position: "FWD"},
		{PickNumber: 2, Round: 1, Team: 2, PlayerID: 2, Position: "MID"},
		{PickNumber: 3, Round: 1, Team: 3, PlayerID: 3, Position: "GK"},
	}

	if err := m.saveFinalResult(); err != nil {
		t.Fatalf("saveFinalResult: %v", err)
	}
	t.Cleanup(func() { os.Remove(m.resultPath) })

	raw, err := os.ReadFile(m.resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var res draftResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if res.Season != 2026 || res.LeagueSize != 8 || res.MyTeam != 3 {
		t.Errorf("metadata mismatch: %+v", res)
	}
	if len(res.Teams) != 8 {
		t.Fatalf("expected 8 teams, got %d", len(res.Teams))
	}
	if len(res.Teams[0].Players) != 1 || res.Teams[0].Players[0].PlayerID != 1 {
		t.Errorf("team 1 mismatch: %+v", res.Teams[0])
	}
	if len(res.Teams[2].Players) != 1 || res.Teams[2].Players[0].PlayerID != 3 {
		t.Errorf("team 3 mismatch: %+v", res.Teams[2])
	}
	if len(res.Picks) != 3 {
		t.Errorf("expected 3 picks, got %d", len(res.Picks))
	}
}
