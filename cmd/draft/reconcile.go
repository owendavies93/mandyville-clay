package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"github.com/mandyville/mandyville-draft/projection"
)

const (
	fplDraftBaseURL = "https://draft.premierleague.com/api"
)

// fplChoice is a single pick from the FPL Draft API.
type fplChoice struct {
	Index     int    `json:"index"`
	Round     int    `json:"round"`
	Pick      int    `json:"pick"`
	Element   int    `json:"element"`
	EntryName string `json:"entry_name"`
	Entry     int    `json:"entry"`
	WasAuto   bool   `json:"was_auto"`
}

type fplChoicesResponse struct {
	Choices []fplChoice `json:"choices"`
}

// fplLeagueEntry is a manager in a draft league.
type fplLeagueEntry struct {
	EntryID   int    `json:"entry_id"`
	ID        int    `json:"id"`
	EntryName string `json:"entry_name"`
}

type fplLeagueResponse struct {
	League struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		DraftStatus string `json:"draft_status"`
		Scoring     string `json:"scoring"`
	} `json:"league"`
	LeagueEntries []fplLeagueEntry `json:"league_entries"`
}

// reconcileDiff describes a single pick that differs between the saved
// result and the FPL API.
type reconcileDiff struct {
	PickNumber int
	Round      int
	EntryName  string
	OurPlayer  string
	OurID      int
	FPLPlayer  string
	FPLID      int
}

func runReconcile(resultPath string, leagueID int, apply bool) error {
	// Load our saved draft result.
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		return fmt.Errorf("reading result file: %w", err)
	}
	var result draftResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("parsing result file: %w", err)
	}

	// Fetch the FPL draft choices.
	choices, err := fetchFPLChoices(leagueID)
	if err != nil {
		return fmt.Errorf("fetching FPL draft: %w", err)
	}

	// Build element → player_id mapping from the database.
	cfg, err := projection.LoadDBConfigFromFile("config.yaml")
	if err != nil {
		// Fall back to default connection.
		cfg = projection.DBConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "postgres",
			Password: "password",
			DBName:   "mandyville",
		}
	}
	db, err := projection.OpenDB(cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer db.Close()

	elemToPlayer, err := loadFPLElementMap(db, result.Season)
	if err != nil {
		return fmt.Errorf("loading FPL element mapping: %w", err)
	}

	// Index our picks by pick number.
	ourPickByNum := make(map[int]*draftPick, len(result.Picks))
	for i := range result.Picks {
		ourPickByNum[result.Picks[i].PickNumber] = &result.Picks[i]
	}

	// Index our players by ID for display names.
	ourPlayerName := make(map[int]string)
	for _, team := range result.Teams {
		for _, p := range team.Players {
			name := fmt.Sprintf("%s %s", p.FirstName, p.LastName)
			ourPlayerName[p.PlayerID] = trimName(name)
		}
	}

	// Compare.
	var diffs []reconcileDiff
	for _, fc := range choices {
		ourPick, ok := ourPickByNum[fc.Index]
		if !ok {
			continue
		}

		fplPlayer, ok := elemToPlayer[fc.Element]
		if !ok {
			diffs = append(diffs, reconcileDiff{
				PickNumber: fc.Index,
				Round:      fc.Round,
				EntryName:  fc.EntryName,
				OurPlayer:  ourPlayerName[ourPick.PlayerID],
				OurID:      ourPick.PlayerID,
				FPLPlayer:  fmt.Sprintf("unknown (element %d)", fc.Element),
				FPLID:      0,
			})
			continue
		}

		if fplPlayer.PlayerID != ourPick.PlayerID {
			diffs = append(diffs, reconcileDiff{
				PickNumber: fc.Index,
				Round:      fc.Round,
				EntryName:  fc.EntryName,
				OurPlayer:  ourPlayerName[ourPick.PlayerID],
				OurID:      ourPick.PlayerID,
				FPLPlayer:  trimName(fmt.Sprintf("%s %s", fplPlayer.FirstName, fplPlayer.LastName)),
				FPLID:      fplPlayer.PlayerID,
			})
		}
	}

	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].PickNumber < diffs[j].PickNumber
	})

	if len(diffs) == 0 {
		fmt.Println("No discrepancies — all picks match.")
		return nil
	}

	fmt.Printf("%d discrepancies found:\n\n", len(diffs))
	fmt.Printf("  %-6s %-5s %-25s %-25s %-25s\n", "Pick", "Round", "Team", "Ours", "FPL (correct)")
	fmt.Printf("  %-6s %-5s %-25s %-25s %-25s\n", "----", "-----", "----", "----", "-------------")
	for _, d := range diffs {
		fmt.Printf("  %-6d R%-4d %-25s %-25s %-25s\n",
			d.PickNumber, d.Round, truncate(d.EntryName, 25),
			d.OurPlayer, d.FPLPlayer)
	}

	if !apply {
		fmt.Printf("\nRun with -apply to patch the result file.\n")
		return nil
	}

	// Apply corrections.
	patched := applyDiffs(result, diffs, elemToPlayer, choices)

	out, err := json.MarshalIndent(patched, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling patched result: %w", err)
	}
	if err := os.WriteFile(resultPath, out, 0o644); err != nil {
		return fmt.Errorf("writing patched result: %w", err)
	}

	fmt.Printf("\nPatched %d picks in %s\n", len(diffs), resultPath)
	return nil
}

// applyDiffs corrects the result to match the FPL API data.
func applyDiffs(result draftResult, diffs []reconcileDiff, elemMap map[int]fplPlayerInfo, choices []fplChoice) draftResult {
	// Build FPL choice index by pick number.
	fplByPick := make(map[int]fplChoice, len(choices))
	for _, c := range choices {
		fplByPick[c.Index] = c
	}

	// Correct picks.
	for i := range result.Picks {
		pk := &result.Picks[i]
		fc, ok := fplByPick[pk.PickNumber]
		if !ok {
			continue
		}
		fp, ok := elemMap[fc.Element]
		if !ok {
			continue
		}
		if pk.PlayerID != fp.PlayerID {
			pk.PlayerID = fp.PlayerID
			pk.Position = fp.Position
		}
	}

	// Rebuild teams from corrected picks.
	teamPlayers := make(map[int][]resultPlayer)
	for _, pk := range result.Picks {
		fc, ok := fplByPick[pk.PickNumber]
		if !ok {
			continue
		}
		fp, ok := elemMap[fc.Element]
		if !ok {
			continue
		}
		teamPlayers[pk.Team] = append(teamPlayers[pk.Team], resultPlayer{
			PlayerID:  fp.PlayerID,
			FirstName: fp.FirstName,
			LastName:  fp.LastName,
			Position:  fp.Position,
			TeamName:  fp.TeamName,
		})
	}

	for i := range result.Teams {
		t := result.Teams[i].Team
		if players, ok := teamPlayers[t]; ok {
			result.Teams[i].Players = players
		}
	}

	return result
}

type fplPlayerInfo struct {
	PlayerID  int
	FirstName string
	LastName  string
	Position  string
	TeamName  string
}

func loadFPLElementMap(db *sql.DB, season int) (map[int]fplPlayerInfo, error) {
	// Draft elements are keyed by fpl_draft_id, which differs from the
	// classic fpl_season_id for a handful of players each season.
	query := `
		SELECT fsi.fpl_draft_id, fsi.player_id,
		       p.first_name, p.last_name,
		       COALESCE(
		           CASE fsi.fpl_positions_id
		               WHEN 1 THEN 'GK'
		               WHEN 2 THEN 'DEF'
		               WHEN 3 THEN 'MID'
		               WHEN 4 THEN 'FWD'
		           END, ''),
		       COALESCE(t.name, '')
		FROM fpl_season_info fsi
		JOIN players p ON p.id = fsi.player_id
		LEFT JOIN players_teams pt ON pt.player_id = fsi.player_id
		    AND pt.start_date <= CURRENT_DATE
		    AND (pt.end_date IS NULL OR pt.end_date >= CURRENT_DATE)
		LEFT JOIN teams t ON t.id = pt.team_id
		WHERE fsi.season = $1
		  AND fsi.fpl_draft_id IS NOT NULL
	`
	rows, err := db.Query(query, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[int]fplPlayerInfo)
	for rows.Next() {
		var elemID int
		var info fplPlayerInfo
		if err := rows.Scan(&elemID, &info.PlayerID, &info.FirstName, &info.LastName, &info.Position, &info.TeamName); err != nil {
			return nil, err
		}
		m[elemID] = info
	}
	return m, nil
}

func fetchFPLChoices(leagueID int) ([]fplChoice, error) {
	url := fmt.Sprintf("%s/draft/%d/choices", fplDraftBaseURL, leagueID)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("FPL API returned %d: %s", resp.StatusCode, string(body))
	}

	var data fplChoicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return data.Choices, nil
}

func trimName(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
