package classic

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// RecommendationRun is one classic cmd/transfers invocation, ready to be
// persisted for later grading.
type RecommendationRun struct {
	ID              int
	EntryID         int // internal fpl_classic_entries.id
	Event           int // upcoming gameweek
	Horizon         int
	BeamWidth       int
	MaxTransfers    int
	MinGain         float64
	PlanValue       float64
	BaselineValue   float64
	FreeTransfers   int
	Bank            int
	ProjectionRunID *int
	RunTime         time.Time
	Rows            []RecommendationRow
}

// RecommendationRow is a single logged recommendation: a transfer, the
// captain, a starting player or a bench player for a gameweek.
type RecommendationRow struct {
	StepEvent    int
	Kind         string // "transfer", "captain", "start", "bench"
	PlayerInID   int
	PlayerOutID  int
	ElementIn    int
	ElementOut   int
	Position     string
	HitCost      int
	ExpectedGain float64
	IsImmediate  bool
}

// Build derives the recommendation rows from a plan: the transfers, captain,
// starting XI and bench for every gameweek of the recommended plan.
func (r *RecommendationRun) Build(outcome *Outcome, pool *Pool, squad *Squad) {
	rec := outcome.Recommended
	if rec == nil {
		return
	}
	baseline := 0.0
	if outcome.RollThisWeek != nil {
		baseline = outcome.RollThisWeek.Total
	}

	for _, step := range rec.Steps {
		immediate := step.GW == r.Event

		// Transfers: the first FreeUsed are free, the rest cost 4 each.
		for i, m := range step.Moves {
			hit := 0
			if i >= step.FreeUsed {
				hit = 4
			}
			gain := 0.0
			if immediate {
				gain = rec.Total - baseline
			}
			row := RecommendationRow{
				StepEvent:    step.GW,
				Kind:         "transfer",
				PlayerInID:   pool.ByElement[m.InElement].PlayerID,
				PlayerOutID:  m.Out.PlayerID,
				ElementIn:    m.InElement,
				ElementOut:   m.OutElement,
				Position:     m.Position,
				HitCost:      hit,
				ExpectedGain: gain,
				IsImmediate:  immediate,
			}
			r.Rows = append(r.Rows, row)
		}

		// Captain, starters and bench.
		points := func(id int) float64 {
			for _, m := range squad.Members {
				if m.PlayerID == id {
					if p := m.Player; p != nil {
						return p.PointsIn(step.GW)
					}
				}
			}
			if pp, ok := pool.ByPlayer[id]; ok {
				if p := pool.ByElement[pp]; p != nil && p.Player != nil {
					return p.Player.PointsIn(step.GW)
				}
			}
			return 0
		}
		positionOf := func(id int) string {
			for _, m := range squad.Members {
				if m.PlayerID == id {
					return m.Position
				}
			}
			if elem, ok := pool.ByPlayer[id]; ok {
				if pp := pool.ByElement[elem]; pp != nil && pp.Player != nil {
					return pp.Player.Position
				}
			}
			return ""
		}

		r.Rows = append(r.Rows, RecommendationRow{
			StepEvent: step.GW, Kind: "captain", PlayerInID: step.Captain,
			Position: positionOf(step.Captain), ExpectedGain: points(step.Captain),
			IsImmediate: immediate,
		})
		for _, id := range step.XI {
			r.Rows = append(r.Rows, RecommendationRow{
				StepEvent: step.GW, Kind: "start", PlayerInID: id,
				Position: positionOf(id), ExpectedGain: points(id),
				IsImmediate: immediate,
			})
		}
		for _, id := range step.Bench {
			r.Rows = append(r.Rows, RecommendationRow{
				StepEvent: step.GW, Kind: "bench", PlayerInID: id,
				Position: positionOf(id), ExpectedGain: points(id),
				IsImmediate: immediate,
			})
		}
	}
}

// SaveRecommendationRun writes a run and its rows in one transaction,
// returning the new run id.
func SaveRecommendationRun(db *sql.DB, run *RecommendationRun) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	var runID int
	err = tx.QueryRow(`
		INSERT INTO fpl_classic_recommendation_runs
		    (classic_entry_id, event, horizon, beam_width, max_transfers,
		     min_gain, plan_value, baseline_value, free_transfers, bank,
		     projection_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`, run.EntryID, run.Event, run.Horizon, run.BeamWidth, run.MaxTransfers,
		run.MinGain, run.PlanValue, run.BaselineValue, run.FreeTransfers,
		run.Bank, nullInt(run.ProjectionRunID)).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("inserting recommendation run: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO fpl_classic_recommendations (
			run_id, step_event, kind, player_in_id, player_out_id,
			element_in, element_out, position, hit_cost, expected_gain,
			is_immediate
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`)
	if err != nil {
		return 0, fmt.Errorf("preparing recommendation insert: %w", err)
	}
	defer stmt.Close()

	for _, r := range run.Rows {
		if _, err := stmt.Exec(
			runID, r.StepEvent, r.Kind, nullInt(&r.PlayerInID), nullInt(&r.PlayerOutID),
			r.ElementIn, r.ElementOut, r.Position, r.HitCost, r.ExpectedGain,
			r.IsImmediate,
		); err != nil {
			return 0, fmt.Errorf("inserting recommendation row: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing recommendation run: %w", err)
	}
	return runID, nil
}

func nullInt(v *int) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

// LoadRecommendationRuns returns recent classic recommendation runs for an
// entry, newest first, with their headline rows loaded.
func LoadRecommendationRuns(db *sql.DB, entryID, limit int) ([]RecommendationRun, error) {
	rows, err := db.Query(`
		SELECT id, classic_entry_id, event, run_time, horizon, beam_width,
		       max_transfers, min_gain, plan_value, baseline_value,
		       free_transfers, bank, projection_run_id
		FROM fpl_classic_recommendation_runs
		WHERE classic_entry_id = $1
		ORDER BY run_time DESC, id DESC
		LIMIT $2`, entryID, limit)
	if err != nil {
		return nil, fmt.Errorf("loading classic recommendation runs: %w", err)
	}
	defer rows.Close()

	var out []RecommendationRun
	for rows.Next() {
		var r RecommendationRun
		var projRun sql.NullInt64
		if err := rows.Scan(&r.ID, &r.EntryID, &r.Event, &r.RunTime,
			&r.Horizon, &r.BeamWidth, &r.MaxTransfers, &r.MinGain,
			&r.PlanValue, &r.BaselineValue, &r.FreeTransfers, &r.Bank,
			&projRun); err != nil {
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

// LoadRecommendationRun loads a single run by ID with all its rows.
func LoadRecommendationRun(db *sql.DB, runID int) (*RecommendationRun, error) {
	var r RecommendationRun
	var projRun sql.NullInt64
	err := db.QueryRow(`
		SELECT id, classic_entry_id, event, run_time, horizon, beam_width,
		       max_transfers, min_gain, plan_value, baseline_value,
		       free_transfers, bank, projection_run_id
		FROM fpl_classic_recommendation_runs
		WHERE id = $1`, runID).Scan(&r.ID, &r.EntryID, &r.Event, &r.RunTime,
		&r.Horizon, &r.BeamWidth, &r.MaxTransfers, &r.MinGain,
		&r.PlanValue, &r.BaselineValue, &r.FreeTransfers, &r.Bank,
		&projRun)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading classic recommendation run %d: %w", runID, err)
	}
	if projRun.Valid {
		v := int(projRun.Int64)
		r.ProjectionRunID = &v
	}

	r.Rows, err = loadClassicRunRows(db, r.ID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LoadLatestRunForEvent loads the most recent run for a given gameweek,
// with all its rows. Returns nil if none exists.
func LoadLatestRunForEvent(db *sql.DB, entryID, event int) (*RecommendationRun, error) {
	var runID int
	err := db.QueryRow(`
		SELECT id FROM fpl_classic_recommendation_runs
		WHERE classic_entry_id = $1 AND event = $2
		ORDER BY run_time DESC LIMIT 1`, entryID, event).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding latest classic run for GW%d: %w", event, err)
	}
	return LoadRecommendationRun(db, runID)
}

// LoadRunHeadlineRows loads only the immediate transfer rows for a set of
// runs (for listing headlines without fetching every row).
func LoadRunHeadlineRows(db *sql.DB, runIDs []int) (map[int][]RecommendationRow, error) {
	if len(runIDs) == 0 {
		return map[int][]RecommendationRow{}, nil
	}
	rows, err := db.Query(`
		SELECT run_id, step_event, kind,
		       COALESCE(player_in_id, 0), COALESCE(player_out_id, 0),
		       COALESCE(element_in, 0), COALESCE(element_out, 0),
		       COALESCE(position, ''), hit_cost, COALESCE(expected_gain, 0),
		       is_immediate
		FROM fpl_classic_recommendations
		WHERE run_id = ANY($1) AND is_immediate AND kind = 'transfer'
		ORDER BY run_id, id`, pq.Array(runIDs))
	if err != nil {
		return nil, fmt.Errorf("loading headline rows: %w", err)
	}
	defer rows.Close()

	out := map[int][]RecommendationRow{}
	for rows.Next() {
		var runID int
		var r RecommendationRow
		if err := rows.Scan(&runID, &r.StepEvent, &r.Kind,
			&r.PlayerInID, &r.PlayerOutID, &r.ElementIn, &r.ElementOut,
			&r.Position, &r.HitCost, &r.ExpectedGain, &r.IsImmediate); err != nil {
			return nil, err
		}
		out[runID] = append(out[runID], r)
	}
	return out, rows.Err()
}

func loadClassicRunRows(db *sql.DB, runID int) ([]RecommendationRow, error) {
	rows, err := db.Query(`
		SELECT step_event, kind,
		       COALESCE(player_in_id, 0), COALESCE(player_out_id, 0),
		       COALESCE(element_in, 0), COALESCE(element_out, 0),
		       COALESCE(position, ''), hit_cost, COALESCE(expected_gain, 0),
		       is_immediate
		FROM fpl_classic_recommendations
		WHERE run_id = $1
		ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("loading classic recommendation rows: %w", err)
	}
	defer rows.Close()

	var out []RecommendationRow
	for rows.Next() {
		var r RecommendationRow
		if err := rows.Scan(&r.StepEvent, &r.Kind,
			&r.PlayerInID, &r.PlayerOutID, &r.ElementIn, &r.ElementOut,
			&r.Position, &r.HitCost, &r.ExpectedGain, &r.IsImmediate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
