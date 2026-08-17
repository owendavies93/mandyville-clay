package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// draftPick records a single selection, including which team made it.
type draftPick struct {
	PickNumber int    `json:"pick_number"`
	Round      int    `json:"round"`
	Team       int    `json:"team"`
	PlayerID   int    `json:"player_id"`
	Position   string `json:"position"`
}

// draftResult is the machine-readable final draft state, suitable for
// loading into other tools.
type draftResult struct {
	Season     int          `json:"season"`
	LeagueSize int          `json:"league_size"`
	MyTeam     int          `json:"my_team"`
	Teams      []resultTeam `json:"teams"`
	Picks      []draftPick  `json:"picks"`
}

type resultTeam struct {
	Team    int            `json:"team"`
	Players []resultPlayer `json:"players"`
}

type resultPlayer struct {
	PlayerID  int    `json:"player_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Position  string `json:"position"`
	TeamName  string `json:"team_name"`
}

// ensureFinalResultSaved writes the final draft state if the draft is
// complete and it hasn't been saved yet. Used as a fallback on quit so a
// resumed, already-complete draft still produces its result file.
func (m *model) ensureFinalResultSaved() {
	if m.isDraftComplete() && m.resultPath == "" {
		_ = m.saveFinalResult()
	}
}

// saveFinalResult writes the completed draft to a JSON file in the working
// directory and records the path on the model.
func (m *model) saveFinalResult() error {
	playerByID := make(map[int]Player, len(m.data.Players))
	for _, p := range m.data.Players {
		playerByID[p.PlayerID] = p
	}

	teams := make([]resultTeam, m.leagueSize)
	for i := range teams {
		teams[i].Team = i + 1
	}
	for _, pk := range m.picks {
		if pk.Team < 1 || pk.Team > m.leagueSize {
			continue
		}
		p, ok := playerByID[pk.PlayerID]
		if !ok {
			continue
		}
		teams[pk.Team-1].Players = append(teams[pk.Team-1].Players, resultPlayer{
			PlayerID:  p.PlayerID,
			FirstName: p.FirstName,
			LastName:  p.LastName,
			Position:  p.Position,
			TeamName:  p.TeamName,
		})
	}

	res := draftResult{
		Season:     m.data.Season,
		LeagueSize: m.leagueSize,
		MyTeam:     m.draftPos,
		Teams:      teams,
		Picks:      m.picks,
	}

	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}

	dir := "out"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := filepath.Join(dir, fmt.Sprintf("draft_result_%d_%s.json", m.data.Season, newSessionID()))
	path := name
	if abs, err := filepath.Abs(name); err == nil {
		path = abs
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}

	m.resultPath = path
	return nil
}
