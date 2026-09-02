package draft

import (
	"database/sql"
	"fmt"
	"time"
)

// RecommendationRun is one cmd/transfers invocation: its parameters and the
// candidate swaps it evaluated, ready to be persisted for later grading.
type RecommendationRun struct {
	ID              int
	LeagueID        int // internal fpl_draft_leagues.id
	EntryID         int // internal fpl_draft_entries.id
	Season          int
	RunTime         time.Time
	Event           int // upcoming gameweek
	Horizon         int
	Discount        float64
	ProjectionRunID *int
	Candidates      []LoggedCandidate
}

// LoggedCandidate is a single evaluated swap as written to the database.
type LoggedCandidate struct {
	PlayerInID         int
	PlayerOutID        int
	ElementIn          int
	ElementOut         int
	Position           string
	Kind               string // "free-agent" or "waiver"
	ExpectedGain       float64
	UndiscountedGain   float64
	H2HGain            *float64
	SuccessProbability *float64
	ClaimOrder         *int
	Recommended        bool
}

// SaveRecommendationRun writes a run and its candidate rows in one
// transaction, returning the new run id.
func SaveRecommendationRun(db *sql.DB, run *RecommendationRun) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	var runID int
	err = tx.QueryRow(`
		INSERT INTO fpl_draft_recommendation_runs
		    (league_id, draft_entry_id, event, horizon, discount, projection_run_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, run.LeagueID, run.EntryID, run.Event, run.Horizon, run.Discount,
		nullInt(run.ProjectionRunID)).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("inserting recommendation run: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO fpl_draft_recommendations (
			run_id, player_in_id, player_out_id, element_in, element_out,
			position, kind, expected_gain, undiscounted_gain, h2h_gain,
			success_probability, claim_order, recommended
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`)
	if err != nil {
		return 0, fmt.Errorf("preparing recommendation insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range run.Candidates {
		if _, err := stmt.Exec(
			runID, c.PlayerInID, c.PlayerOutID, c.ElementIn, c.ElementOut,
			c.Position, c.Kind, c.ExpectedGain, c.UndiscountedGain,
			nullFloat(c.H2HGain), nullFloat(c.SuccessProbability),
			nullInt(c.ClaimOrder), c.Recommended,
		); err != nil {
			return 0, fmt.Errorf("inserting recommendation %d -> %d: %w", c.PlayerOutID, c.PlayerInID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing recommendation run: %w", err)
	}
	return runID, nil
}

// LoadRecommendationRuns loads every persisted run with its candidates,
// newest first. Used by cmd/backtest -grade-recommendations.
func LoadRecommendationRuns(db *sql.DB) ([]RecommendationRun, error) {
	rows, err := db.Query(`
		SELECT rr.id, rr.league_id, rr.draft_entry_id, rr.event,
		       rr.horizon, rr.discount, rr.projection_run_id,
		       rr.run_time, l.season
		FROM fpl_draft_recommendation_runs rr
		JOIN fpl_draft_leagues l ON l.id = rr.league_id
		ORDER BY rr.run_time DESC, rr.id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("loading recommendation runs: %w", err)
	}
	defer rows.Close()

	var runs []RecommendationRun
	for rows.Next() {
		var r RecommendationRun
		var projRun sql.NullInt64
		if err := rows.Scan(&r.ID, &r.LeagueID, &r.EntryID, &r.Event,
			&r.Horizon, &r.Discount, &projRun, &r.RunTime, &r.Season); err != nil {
			return nil, err
		}
		if projRun.Valid {
			v := int(projRun.Int64)
			r.ProjectionRunID = &v
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range runs {
		cands, err := loadRunCandidates(db, runs[i].ID)
		if err != nil {
			return nil, err
		}
		runs[i].Candidates = cands
	}
	return runs, nil
}

// LoadRunCandidates loads the candidate rows for a single run.
func LoadRunCandidates(db *sql.DB, runID int) ([]LoggedCandidate, error) {
	return loadRunCandidates(db, runID)
}

func loadRunCandidates(db *sql.DB, runID int) ([]LoggedCandidate, error) {
	rows, err := db.Query(`
		SELECT COALESCE(player_in_id, 0), COALESCE(player_out_id, 0),
		       COALESCE(element_in, 0), COALESCE(element_out, 0),
		       position, kind, expected_gain, undiscounted_gain,
		       h2h_gain, success_probability, claim_order, recommended
		FROM fpl_draft_recommendations
		WHERE run_id = $1
		ORDER BY id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("loading recommendation rows: %w", err)
	}
	defer rows.Close()

	var out []LoggedCandidate
	for rows.Next() {
		var c LoggedCandidate
		var h2h, prob sql.NullFloat64
		var order sql.NullInt64
		if err := rows.Scan(&c.PlayerInID, &c.PlayerOutID, &c.ElementIn, &c.ElementOut,
			&c.Position, &c.Kind, &c.ExpectedGain, &c.UndiscountedGain,
			&h2h, &prob, &order, &c.Recommended); err != nil {
			return nil, err
		}
		if h2h.Valid {
			v := h2h.Float64
			c.H2HGain = &v
		}
		if prob.Valid {
			v := prob.Float64
			c.SuccessProbability = &v
		}
		if order.Valid {
			v := int(order.Int64)
			c.ClaimOrder = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// nullInt converts a *int to a nullable SQL value.
func nullInt(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// nullFloat converts a *float64 to a nullable SQL value.
func nullFloat(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// LoadRecentRuns returns recent draft recommendation runs for a league and
// entry, newest first.
func LoadRecentRuns(db *sql.DB, leagueID, entryID, limit int) ([]RecommendationRun, error) {
	rows, err := db.Query(`
		SELECT rr.id, rr.league_id, rr.draft_entry_id, rr.event,
		       rr.horizon, rr.discount, rr.projection_run_id,
		       rr.run_time, l.season
		FROM fpl_draft_recommendation_runs rr
		JOIN fpl_draft_leagues l ON l.id = rr.league_id
		WHERE rr.league_id = $1 AND rr.draft_entry_id = $2
		ORDER BY rr.run_time DESC, rr.id DESC
		LIMIT $3
	`, leagueID, entryID, limit)
	if err != nil {
		return nil, fmt.Errorf("loading recent draft recommendation runs: %w", err)
	}
	defer rows.Close()

	var out []RecommendationRun
	for rows.Next() {
		var r RecommendationRun
		var projRun sql.NullInt64
		if err := rows.Scan(&r.ID, &r.LeagueID, &r.EntryID, &r.Event,
			&r.Horizon, &r.Discount, &projRun, &r.RunTime, &r.Season); err != nil {
			return nil, err
		}
		if projRun.Valid {
			v := int(projRun.Int64)
			r.ProjectionRunID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadSingleRun loads a single recommendation run by ID with all its
// candidates.
func LoadSingleRun(db *sql.DB, runID int) (*RecommendationRun, error) {
	var r RecommendationRun
	var projRun sql.NullInt64
	err := db.QueryRow(`
		SELECT rr.id, rr.league_id, rr.draft_entry_id, rr.event,
		       rr.horizon, rr.discount, rr.projection_run_id,
		       rr.run_time, l.season
		FROM fpl_draft_recommendation_runs rr
		JOIN fpl_draft_leagues l ON l.id = rr.league_id
		WHERE rr.id = $1
	`, runID).Scan(&r.ID, &r.LeagueID, &r.EntryID, &r.Event,
		&r.Horizon, &r.Discount, &projRun, &r.RunTime, &r.Season)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading draft recommendation run %d: %w", runID, err)
	}
	if projRun.Valid {
		v := int(projRun.Int64)
		r.ProjectionRunID = &v
	}

	r.Candidates, err = loadRunCandidates(db, r.ID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LoadLatestRunForEvent loads the most recent draft run for a given league,
// entry and gameweek, with all its candidates. Returns nil if none exists.
func LoadLatestRunForEvent(db *sql.DB, leagueID, entryID, event int) (*RecommendationRun, error) {
	var runID int
	err := db.QueryRow(`
		SELECT id FROM fpl_draft_recommendation_runs
		WHERE league_id = $1 AND draft_entry_id = $2 AND event = $3
		ORDER BY run_time DESC LIMIT 1
	`, leagueID, entryID, event).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding latest draft run for GW%d: %w", event, err)
	}
	return LoadSingleRun(db, runID)
}
