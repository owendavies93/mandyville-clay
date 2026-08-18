package draft

import (
	"database/sql"
	"fmt"
)

// LoadLeagues returns all draft leagues stored for a season.
func LoadLeagues(db *sql.DB, season int) ([]League, error) {
	rows, err := db.Query(`
		SELECT id, fpl_league_id, season, name,
		       COALESCE(scoring, ''), COALESCE(transaction_mode, ''),
		       COALESCE(trades, ''), COALESCE(start_event, 0), COALESCE(stop_event, 0)
		FROM fpl_draft_leagues
		WHERE season = $1
		ORDER BY id
	`, season)
	if err != nil {
		return nil, fmt.Errorf("loading draft leagues: %w", err)
	}
	defer rows.Close()

	var out []League
	for rows.Next() {
		var l League
		if err := rows.Scan(&l.ID, &l.FPLID, &l.Season, &l.Name,
			&l.Scoring, &l.TransactionMode, &l.Trades,
			&l.StartEvent, &l.StopEvent); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LoadEntries returns all managers for a league.
func LoadEntries(db *sql.DB, leagueID int) ([]Entry, error) {
	rows, err := db.Query(`
		SELECT id, league_id, entry_id, league_entry_id,
		       entry_name, COALESCE(short_name, ''), is_mine
		FROM fpl_draft_entries
		WHERE league_id = $1
		ORDER BY id
	`, leagueID)
	if err != nil {
		return nil, fmt.Errorf("loading draft entries: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.LeagueID, &e.EntryID, &e.LeagueEntryID,
			&e.Name, &e.ShortName, &e.IsMine); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LoadOwnership returns the open ownership rows for a league (one per
// element). Free agents have EntryID 0; unmatched elements have PlayerID 0.
func LoadOwnership(db *sql.DB, leagueID int) ([]Ownership, error) {
	rows, err := db.Query(`
		SELECT league_id, COALESCE(draft_entry_id, 0), COALESCE(player_id, 0),
		       fpl_draft_element, COALESCE(status, ''),
		       in_accepted_trade
		FROM fpl_draft_ownership
		WHERE league_id = $1 AND end_time IS NULL
	`, leagueID)
	if err != nil {
		return nil, fmt.Errorf("loading draft ownership: %w", err)
	}
	defer rows.Close()

	var out []Ownership
	for rows.Next() {
		var o Ownership
		if err := rows.Scan(&o.LeagueID, &o.EntryID, &o.PlayerID, &o.Element,
			&o.Status, &o.InTrade); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LoadWaiverOrder returns the open waiver-order rows for a league.
func LoadWaiverOrder(db *sql.DB, leagueID int) ([]WaiverOrder, error) {
	rows, err := db.Query(`
		SELECT wo.draft_entry_id, wo.waiver_pick
		FROM fpl_draft_waiver_order wo
		JOIN fpl_draft_entries e ON e.id = wo.draft_entry_id
		WHERE e.league_id = $1 AND wo.end_time IS NULL
		ORDER BY wo.waiver_pick, wo.draft_entry_id
	`, leagueID)
	if err != nil {
		return nil, fmt.Errorf("loading waiver order: %w", err)
	}
	defer rows.Close()

	var out []WaiverOrder
	for rows.Next() {
		var w WaiverOrder
		if err := rows.Scan(&w.EntryID, &w.WaiverPick); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// LoadEntryPicks returns each entry's squad slots for a gameweek, keyed by
// entry id. Empty until the cron has synced that gameweek's lineups.
func LoadEntryPicks(db *sql.DB, leagueID, event int) (map[int][]PickSlot, error) {
	rows, err := db.Query(`
		SELECT draft_entry_id, COALESCE(player_id, 0), fpl_draft_element,
		       position, is_starting
		FROM fpl_draft_entry_picks
		WHERE league_id = $1 AND event = $2
		ORDER BY draft_entry_id, position
	`, leagueID, event)
	if err != nil {
		return nil, fmt.Errorf("loading entry picks: %w", err)
	}
	defer rows.Close()

	out := make(map[int][]PickSlot)
	for rows.Next() {
		var entryID int
		var p PickSlot
		if err := rows.Scan(&entryID, &p.PlayerID, &p.Element, &p.Position, &p.IsStarting); err != nil {
			return nil, err
		}
		out[entryID] = append(out[entryID], p)
	}
	return out, rows.Err()
}

// ElementAvailability is the open availability row for a draft element,
// used to describe unmatched free agents (no player match) to the user.
type ElementAvailability struct {
	Element   int
	Status    string
	DraftRank int
	News      string
}

// LoadElementAvailability returns the open availability rows for a set of
// draft elements in a season, keyed by element id.
func LoadElementAvailability(db *sql.DB, season int, elements []int) (map[int]ElementAvailability, error) {
	if len(elements) == 0 {
		return map[int]ElementAvailability{}, nil
	}
	rows, err := db.Query(`
		SELECT fpl_draft_element, COALESCE(status, ''),
		       COALESCE(draft_rank, 0), COALESCE(news, '')
		FROM fpl_player_availability
		WHERE season = $1 AND end_time IS NULL AND fpl_draft_element = ANY($2)
	`, season, elements)
	if err != nil {
		return nil, fmt.Errorf("loading element availability: %w", err)
	}
	defer rows.Close()

	out := make(map[int]ElementAvailability)
	for rows.Next() {
		var a ElementAvailability
		if err := rows.Scan(&a.Element, &a.Status, &a.DraftRank, &a.News); err != nil {
			return nil, err
		}
		out[a.Element] = a
	}
	return out, rows.Err()
}
