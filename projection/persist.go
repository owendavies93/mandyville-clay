package projection

import (
	"database/sql"
	"fmt"
)

// ProjectionRun is a persisted projection run and its per-player rows.
type ProjectionRun struct {
	ID            int
	Season        int
	Kind          string
	AsOfGameweek  int
	Scoring       string
	EngineVersion string
	Output        *ProjectionOutput
	Prior         map[int]PriorRates
}

// SaveProjectionRun writes a projection run and its per-player rows in a
// single transaction, returning the new run id. prior supplies the rate-level
// quantities for pre-season runs (nil for in-season runs, which don't carry
// a meaningful prior).
func SaveProjectionRun(db *sql.DB, output *ProjectionOutput, prior map[int]PriorRates, scoring, engineVersion string) (int, error) {
	kind := "pre-season"
	var asOfGW interface{} = nil
	if output.AsOfGameweek > 0 {
		kind = "in-season"
		asOfGW = output.AsOfGameweek
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	var runID int
	err = tx.QueryRow(`
		INSERT INTO fpl_projection_runs
		    (season, run_time, kind, as_of_gameweek, scoring, engine_version)
		VALUES ($1, now(), $2, $3, $4, $5)
		RETURNING id
	`, output.Season, kind, asOfGW, scoring, engineVersion).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("inserting projection run: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO fpl_projections (
			run_id, player_id, team_id, projected_minutes,
			xg_per_90, xa_per_90, yellows_per_90, reds_per_90,
			bonus_per_match, minutes_per_fixture,
			projected_goals, projected_assists, projected_clean_sheets,
			projected_bonus, projected_yellows, projected_reds, projected_defcon,
			appearance_points, goal_points, assist_points, clean_sheet_points,
			save_points, bonus_points, card_points, goals_conceded_penalty,
			defcon_points, projected_points, consistency, floor,
			h2h_adjusted_points, vorp
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			$21, $22, $23, $24, $25, $26, $27, $28, $29, $30,
			$31
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("preparing projection insert: %w", err)
	}
	defer stmt.Close()

	for _, p := range output.Players {
		pr := prior[p.PlayerID]
		if _, err := stmt.Exec(
			runID, p.PlayerID, p.TeamID, p.ProjectedMinutes,
			pr.XGPer90, pr.XAPer90, pr.YellowsPer90, pr.RedsPer90,
			pr.BonusPerMatch, pr.MinutesPerFixture,
			p.ProjectedGoals, p.ProjectedAssists, p.ProjectedCleanSheets,
			p.ProjectedBonus, p.ProjectedYellows, p.ProjectedReds, p.ProjectedDEFCON,
			p.AppearancePoints, p.GoalPoints, p.AssistPoints, p.CleanSheetPoints,
			p.SavePoints, p.BonusPoints, p.CardPoints, p.GoalsConcededPen,
			p.DEFCONPoints, p.ProjectedPoints, p.Consistency, p.Floor,
			p.H2HAdjustedPts, p.VORP,
		); err != nil {
			return 0, fmt.Errorf("inserting projection for player %d: %w", p.PlayerID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing projection run: %w", err)
	}

	return runID, nil
}

// LoadProjectionRun loads the most recent run of the given kind for a season,
// reconstructing the projection output and prior rates.
func LoadProjectionRun(db *sql.DB, season int, kind string) (*ProjectionRun, error) {
	run := &ProjectionRun{Season: season, Kind: kind, Prior: make(map[int]PriorRates)}

	var asOfGW sql.NullInt64
	err := db.QueryRow(`
		SELECT id, season, kind, as_of_gameweek, scoring, engine_version
		FROM fpl_projection_runs
		WHERE season = $1 AND kind = $2
		ORDER BY run_time DESC, id DESC
		LIMIT 1
	`, season, kind).Scan(&run.ID, &run.Season, &run.Kind, &asOfGW, &run.Scoring, &run.EngineVersion)
	if err != nil {
		return nil, fmt.Errorf("loading projection run: %w", err)
	}
	if asOfGW.Valid {
		run.AsOfGameweek = int(asOfGW.Int64)
	}

	rows, err := db.Query(`
		SELECT fpl.player_id, fpl.team_id, p.first_name, p.last_name,
		       COALESCE(fsi.fpl_positions_id, 0), COALESCE(t.name, ''),
		       fpl.projected_minutes,
		       fpl.xg_per_90, fpl.xa_per_90, fpl.yellows_per_90, fpl.reds_per_90,
		       fpl.bonus_per_match, fpl.minutes_per_fixture,
		       fpl.projected_goals, fpl.projected_assists, fpl.projected_clean_sheets,
		       fpl.projected_bonus, fpl.projected_yellows, fpl.projected_reds, fpl.projected_defcon,
		       fpl.appearance_points, fpl.goal_points, fpl.assist_points, fpl.clean_sheet_points,
		       fpl.save_points, fpl.bonus_points, fpl.card_points, fpl.goals_conceded_penalty,
		       fpl.defcon_points, fpl.projected_points, fpl.consistency, fpl.floor,
		       fpl.h2h_adjusted_points, fpl.vorp
		FROM fpl_projections fpl
		JOIN players p ON p.id = fpl.player_id
		LEFT JOIN fpl_season_info fsi ON fsi.player_id = fpl.player_id AND fsi.season = $2
		LEFT JOIN teams t ON t.id = fpl.team_id
		WHERE fpl.run_id = $1
		ORDER BY fpl.projected_points DESC
	`, run.ID, season)
	if err != nil {
		return nil, fmt.Errorf("loading projections: %w", err)
	}
	defer rows.Close()

	output := &ProjectionOutput{Season: season, AsOfGameweek: run.AsOfGameweek}
	for rows.Next() {
		var p PlayerProjection
		var pr PriorRates
		var teamID sql.NullInt64
		var posID int
		if err := rows.Scan(
			&p.PlayerID, &teamID, &p.FirstName, &p.LastName, &posID, &p.TeamName,
			&p.ProjectedMinutes,
			&pr.XGPer90, &pr.XAPer90, &pr.YellowsPer90, &pr.RedsPer90,
			&pr.BonusPerMatch, &pr.MinutesPerFixture,
			&p.ProjectedGoals, &p.ProjectedAssists, &p.ProjectedCleanSheets,
			&p.ProjectedBonus, &p.ProjectedYellows, &p.ProjectedReds, &p.ProjectedDEFCON,
			&p.AppearancePoints, &p.GoalPoints, &p.AssistPoints, &p.CleanSheetPoints,
			&p.SavePoints, &p.BonusPoints, &p.CardPoints, &p.GoalsConcededPen,
			&p.DEFCONPoints, &p.ProjectedPoints, &p.Consistency, &p.Floor,
			&p.H2HAdjustedPts, &p.VORP,
		); err != nil {
			return nil, err
		}
		if teamID.Valid {
			p.TeamID = int(teamID.Int64)
		}
		p.Position = FPLPosition(posID).String()
		output.Players = append(output.Players, p)
		run.Prior[p.PlayerID] = pr
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	run.Output = output
	return run, nil
}
