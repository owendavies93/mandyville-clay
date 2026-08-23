package classic

import (
	"database/sql"
	"fmt"
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
