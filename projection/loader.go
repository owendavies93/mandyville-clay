package projection

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

const (
	englishPLCompetitionID = 190
)

// playerFixtureRow is a raw row from the player fixture query.
type playerFixtureRow struct {
	PlayerID      int
	Season        int
	Minutes       int
	Goals         int
	Assists       int
	YellowCard    bool
	RedCard       bool
	XG            sql.NullFloat64
	XA            sql.NullFloat64
	NPXG          sql.NullFloat64
	KeyPasses     sql.NullInt64
	PositionID    sql.NullInt64
	CompetitionID int
	IsPL          bool
}

// fplGWRow is a raw row from the FPL gameweek query.
type fplGWRow struct {
	PlayerID    int
	Season      int
	Gameweek    int
	TotalPoints int
	BonusPoints int
	BPS         int
}

// teamPerfRow is a raw row from the team performance query.
type teamPerfRow struct {
	FixtureID   int
	TeamID      int
	Season      int
	XG          float64
	PPDA        float64
	XPts        float64
	CompetitionID int
}

// LoadActivePlayers loads all active FPL players for the given season.
func LoadActivePlayers(db *sql.DB, season int) ([]Player, error) {
	query := `
		SELECT p.id, p.first_name, p.last_name,
		       fsi.fpl_positions_id
		FROM players p
		JOIN fpl_season_info fsi ON fsi.player_id = p.id
		WHERE fsi.season = $1
		  AND fsi.active = true
	`
	rows, err := db.Query(query, season)
	if err != nil {
		return nil, fmt.Errorf("loading active players: %w", err)
	}
	defer rows.Close()

	var players []Player
	for rows.Next() {
		var p Player
		var posID int
		if err := rows.Scan(&p.ID, &p.FirstName, &p.LastName, &posID); err != nil {
			return nil, err
		}
		p.Position = FPLPosition(posID)
		players = append(players, p)
	}
	return players, rows.Err()
}

// LoadAllFPLPlayers loads all FPL players for the given season (for DBs
// without the active column).
func LoadAllFPLPlayers(db *sql.DB, season int) ([]Player, error) {
	query := `
		SELECT p.id, p.first_name, p.last_name,
		       fsi.fpl_positions_id
		FROM players p
		JOIN fpl_season_info fsi ON fsi.player_id = p.id
		WHERE fsi.season = $1
	`
	rows, err := db.Query(query, season)
	if err != nil {
		return nil, fmt.Errorf("loading FPL players: %w", err)
	}
	defer rows.Close()

	var players []Player
	for rows.Next() {
		var p Player
		var posID int
		if err := rows.Scan(&p.ID, &p.FirstName, &p.LastName, &posID); err != nil {
			return nil, err
		}
		p.Position = FPLPosition(posID)
		players = append(players, p)
	}
	return players, rows.Err()
}

// LoadPlayerFixtures loads all fixture data for a set of players, for
// seasons strictly before the target season. Includes competition info
// so we can distinguish PL from other competitions.
func LoadPlayerFixtures(db *sql.DB, playerIDs []int, beforeSeason int) (map[int][]playerFixtureRow, error) {
	query := `
		SELECT pf.player_id, f.season,
		       pf.minutes, COALESCE(pf.goals, 0), COALESCE(pf.assists, 0),
		       pf.yellow_card, pf.red_card,
		       pf.xg, pf.xa, pf.npxg, pf.key_passes,
		       pf.position_id,
		       f.competition_id
		FROM players_fixtures pf
		JOIN fixtures f ON f.id = pf.fixture_id
		WHERE pf.player_id = ANY($1)
		  AND f.season < $2
		ORDER BY pf.player_id, f.season, f.fixture_date
	`
	rows, err := db.Query(query, playerIDs, beforeSeason)
	if err != nil {
		return nil, fmt.Errorf("loading player fixtures: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]playerFixtureRow)
	for rows.Next() {
		var r playerFixtureRow
		if err := rows.Scan(
			&r.PlayerID, &r.Season,
			&r.Minutes, &r.Goals, &r.Assists,
			&r.YellowCard, &r.RedCard,
			&r.XG, &r.XA, &r.NPXG, &r.KeyPasses,
			&r.PositionID,
			&r.CompetitionID,
		); err != nil {
			return nil, err
		}
		r.IsPL = r.CompetitionID == englishPLCompetitionID
		result[r.PlayerID] = append(result[r.PlayerID], r)
	}
	return result, rows.Err()
}

// LoadFPLGameweeks loads FPL gameweek data for a set of players.
func LoadFPLGameweeks(db *sql.DB, playerIDs []int, beforeSeason int) (map[int][]fplGWRow, error) {
	query := `
		SELECT pg.player_id, g.season, g.gameweek,
		       pg.total_points, pg.bonus_points, pg.bps
		FROM fpl_players_gameweeks pg
		JOIN fpl_gameweeks g ON g.id = pg.fpl_gameweek_id
		WHERE pg.player_id = ANY($1)
		  AND g.season < $2
		ORDER BY pg.player_id, g.season, g.gameweek
	`
	rows, err := db.Query(query, playerIDs, beforeSeason)
	if err != nil {
		return nil, fmt.Errorf("loading FPL gameweeks: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]fplGWRow)
	for rows.Next() {
		var r fplGWRow
		if err := rows.Scan(
			&r.PlayerID, &r.Season, &r.Gameweek,
			&r.TotalPoints, &r.BonusPoints, &r.BPS,
		); err != nil {
			return nil, err
		}
		result[r.PlayerID] = append(result[r.PlayerID], r)
	}
	return result, rows.Err()
}

// LoadTeamStrengths computes team attacking and defensive ratings from
// fixtures_team_performance for all teams that appear in the English PL
// in seasons before the target season.
func LoadTeamStrengths(db *sql.DB, beforeSeason int) (map[int]*TeamStrength, error) {
	// Get xG for and against for each team in each PL match.
	query := `
		SELECT f.id, ftp.team_id, f.season, ftp.xg, ftp.ppda, ftp.xpts,
		       f.competition_id
		FROM fixtures_team_performance ftp
		JOIN fixtures f ON f.id = ftp.fixture_id
		WHERE f.season < $1
		  AND f.competition_id = $2
		ORDER BY ftp.team_id, f.season
	`
	rows, err := db.Query(query, beforeSeason, englishPLCompetitionID)
	if err != nil {
		return nil, fmt.Errorf("loading team performance: %w", err)
	}
	defer rows.Close()

	// Collect raw data per fixture (we need both sides to compute xG against).
	type fixturePerf struct {
		teamID int
		season int
		xg     float64
		ppda   float64
		xpts   float64
	}
	fixtureData := make(map[int][]fixturePerf) // fixture_id -> perfs

	for rows.Next() {
		var fp fixturePerf
		var fixtureID, compID int
		if err := rows.Scan(&fixtureID, &fp.teamID, &fp.season, &fp.xg, &fp.ppda, &fp.xpts, &compID); err != nil {
			return nil, err
		}
		fixtureData[fixtureID] = append(fixtureData[fixtureID], fp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Now compute per-team stats: weight by recency.
	type teamSeasonAgg struct {
		xgFor     float64
		xgAgainst float64
		matches   int
		ppda      float64
	}

	teamSeasons := make(map[int]map[int]*teamSeasonAgg) // teamID -> season -> agg

	for _, perfs := range fixtureData {
		if len(perfs) != 2 {
			continue // need both teams
		}
		for i := 0; i < 2; i++ {
			opp := 1 - i
			tid := perfs[i].teamID
			season := perfs[i].season

			if teamSeasons[tid] == nil {
				teamSeasons[tid] = make(map[int]*teamSeasonAgg)
			}
			if teamSeasons[tid][season] == nil {
				teamSeasons[tid][season] = &teamSeasonAgg{}
			}
			agg := teamSeasons[tid][season]
			agg.xgFor += perfs[i].xg
			agg.xgAgainst += perfs[opp].xg
			agg.ppda += perfs[i].ppda
			agg.matches++
		}
	}

	// Weight seasons: most recent gets 0.5, then 0.3, then 0.2.
	weights := []float64{0.5, 0.3, 0.2}

	result := make(map[int]*TeamStrength)
	for teamID, seasons := range teamSeasons {
		// Sort seasons descending.
		sortedSeasons := make([]int, 0, len(seasons))
		for s := range seasons {
			sortedSeasons = append(sortedSeasons, s)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(sortedSeasons)))

		var totalWeight float64
		var weightedXGFor, weightedXGAgainst, weightedPPDA float64

		for i, s := range sortedSeasons {
			if i >= len(weights) {
				break
			}
			agg := seasons[s]
			if agg.matches == 0 {
				continue
			}
			w := weights[i]
			totalWeight += w
			weightedXGFor += w * (agg.xgFor / float64(agg.matches))
			weightedXGAgainst += w * (agg.xgAgainst / float64(agg.matches))
			weightedPPDA += w * (agg.ppda / float64(agg.matches))
		}

		if totalWeight == 0 {
			continue
		}

		offRating := weightedXGFor / totalWeight
		defRating := weightedXGAgainst / totalWeight

		// Clean sheet probability: use Poisson P(0) = e^(-lambda) where
		// lambda = xG conceded per match.
		csProb := math.Exp(-defRating)

		result[teamID] = &TeamStrength{
			TeamID:                teamID,
			OffensiveRating:       offRating,
			DefensiveRating:       defRating,
			CleanSheetProb:        csProb,
			GoalsConcededPerMatch: defRating,
		}
	}

	// Load team names.
	nameQuery := `SELECT id, name FROM teams WHERE id = ANY($1)`
	teamIDs := make([]int, 0, len(result))
	for tid := range result {
		teamIDs = append(teamIDs, tid)
	}
	nameRows, err := db.Query(nameQuery, teamIDs)
	if err != nil {
		return nil, err
	}
	defer nameRows.Close()
	for nameRows.Next() {
		var id int
		var name string
		if err := nameRows.Scan(&id, &name); err != nil {
			return nil, err
		}
		if ts, ok := result[id]; ok {
			ts.TeamName = name
		}
	}

	return result, nameRows.Err()
}

// PlayerTeamInfo holds a player's current team and whether it's a PL team.
type PlayerTeamInfo struct {
	TeamID   int
	TeamName string
	IsPL     bool
}

// LoadPlayerTeams finds each player's team for the target season using the
// players_teams table as the single source of truth.
//
// For upcoming seasons (non-backtest) it uses the player's current team
// (the players_teams row with no end date). For backtests it uses the team
// the player was at when the summer transfer window closed. Players not
// matched by either query fall back to their most recent team entry.
func LoadPlayerTeams(db *sql.DB, playerIDs []int, targetSeason int, backtest bool) (map[int]PlayerTeamInfo, error) {
	// Get the set of PL teams for the target season.
	plTeams := make(map[int]bool)
	plQuery := `
		SELECT DISTINCT home_team_id FROM fixtures
		WHERE competition_id = $1 AND season = $2
	`
	plRows, err := db.Query(plQuery, englishPLCompetitionID, targetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading PL teams: %w", err)
	}
	defer plRows.Close()
	for plRows.Next() {
		var tid int
		if err := plRows.Scan(&tid); err != nil {
			return nil, err
		}
		plTeams[tid] = true
	}
	if err := plRows.Err(); err != nil {
		return nil, err
	}

	result := make(map[int]PlayerTeamInfo)

	scanTeams := func(query string, args ...interface{}) error {
		rows, err := db.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var playerID, teamID int
			var teamName string
			if err := rows.Scan(&playerID, &teamID, &teamName); err != nil {
				return err
			}
			result[playerID] = PlayerTeamInfo{
				TeamID:   teamID,
				TeamName: teamName,
				IsPL:     plTeams[teamID],
			}
		}
		return rows.Err()
	}

	if backtest {
		// Team the player was at when the summer window closed, so
		// summer transfers are captured but January moves are not.
		cutoff, err := loadTeamCutoffDate(db, targetSeason)
		if err != nil {
			return nil, err
		}
		query := `
			SELECT DISTINCT ON (pt.player_id)
			       pt.player_id, pt.team_id, t.name
			FROM players_teams pt
			JOIN teams t ON t.id = pt.team_id
			WHERE pt.player_id = ANY($1)
			  AND pt.start_date <= $2::date
			  AND (pt.end_date IS NULL OR pt.end_date > $2::date)
			ORDER BY pt.player_id, pt.start_date DESC
		`
		if err := scanTeams(query, playerIDs, cutoff); err != nil {
			return nil, fmt.Errorf("loading season-start teams: %w", err)
		}
	} else {
		// Current team for the upcoming season.
		query := `
			SELECT DISTINCT ON (pt.player_id)
			       pt.player_id, pt.team_id, t.name
			FROM players_teams pt
			JOIN teams t ON t.id = pt.team_id
			WHERE pt.player_id = ANY($1)
			  AND pt.end_date IS NULL
			ORDER BY pt.player_id, pt.start_date DESC
		`
		if err := scanTeams(query, playerIDs); err != nil {
			return nil, fmt.Errorf("loading current teams: %w", err)
		}
	}

	// Fallback: players without a matching entry use their most recent
	// players_teams entry.
	var missing []int
	for _, pid := range playerIDs {
		if _, ok := result[pid]; !ok {
			missing = append(missing, pid)
		}
	}
	if len(missing) > 0 {
		fallbackQuery := `
			SELECT DISTINCT ON (pt.player_id)
			       pt.player_id, pt.team_id, t.name
			FROM players_teams pt
			JOIN teams t ON t.id = pt.team_id
			WHERE pt.player_id = ANY($1)
			ORDER BY pt.player_id, pt.start_date DESC
		`
		if err := scanTeams(fallbackQuery, missing); err != nil {
			return nil, fmt.Errorf("loading fallback teams: %w", err)
		}
	}

	return result, nil
}

// loadTeamCutoffDate returns the date used to anchor "team at season start"
// lookups in players_teams: the first of September in the season's starting
// calendar year. This captures summer transfers (which complete around the
// transfer deadline) while excluding January moves.
func loadTeamCutoffDate(db *sql.DB, season int) (string, error) {
	var d string
	err := db.QueryRow(`
		SELECT (date_trunc('year', min(fixture_date)) + interval '8 months')::date::text
		FROM fixtures
		WHERE competition_id = $1 AND season = $2
	`, englishPLCompetitionID, season).Scan(&d)
	if err != nil {
		return "", fmt.Errorf("loading team cutoff date: %w", err)
	}
	return d, nil
}

// LoadPlayersFromFixtures builds a player pool from everyone who appeared
// in PL fixtures for the given season before the cutoff date. Used for
// backtesting seasons where fpl_season_info is incomplete.
func LoadPlayersFromFixtures(db *sql.DB, season int, beforeDate string) ([]Player, error) {
	// Map position_category to FPL position ID.
	categoryToFPL := map[string]FPLPosition{
		"Goalkeeper": Goalkeeper,
		"Defender":   Defender,
		"Midfielder": Midfielder,
		"Forward":    Forward,
	}

	query := `
		SELECT DISTINCT ON (pf.player_id)
		       pf.player_id, p.first_name, p.last_name,
		       pos.position_category
		FROM players_fixtures pf
		JOIN fixtures f ON f.id = pf.fixture_id
		JOIN players p ON p.id = pf.player_id
		LEFT JOIN positions pos ON pos.id = pf.position_id
		WHERE f.competition_id = $1
		  AND f.season = $2
		  AND f.fixture_date < $3
		  AND pf.minutes > 0
		ORDER BY pf.player_id, f.fixture_date DESC
	`
	rows, err := db.Query(query, englishPLCompetitionID, season, beforeDate)
	if err != nil {
		return nil, fmt.Errorf("loading players from fixtures: %w", err)
	}
	defer rows.Close()

	var players []Player
	for rows.Next() {
		var p Player
		var posCat sql.NullString
		if err := rows.Scan(&p.ID, &p.FirstName, &p.LastName, &posCat); err != nil {
			return nil, err
		}
		if posCat.Valid {
			p.Position = categoryToFPL[posCat.String]
		} else {
			// Try to get position from any other fixture.
			var fallbackCat sql.NullString
			err := db.QueryRow(`
				SELECT pos.position_category
				FROM players_fixtures pf
				JOIN positions pos ON pos.id = pf.position_id
				WHERE pf.player_id = $1 AND pf.position_id IS NOT NULL
				GROUP BY pos.position_category
				ORDER BY count(*) DESC
				LIMIT 1
			`, p.ID).Scan(&fallbackCat)
			if err == nil && fallbackCat.Valid {
				p.Position = categoryToFPL[fallbackCat.String]
			} else {
				continue // skip players with no position data at all
			}
		}
		if p.Position == 0 {
			continue
		}
		players = append(players, p)
	}
	return players, rows.Err()
}

// LoadActualFPLPoints loads actual FPL season totals for a given season
// (for backtesting).
func LoadActualFPLPoints(db *sql.DB, season int) (map[int]int, error) {
	query := `
		SELECT pg.player_id, sum(pg.total_points) as total
		FROM fpl_players_gameweeks pg
		JOIN fpl_gameweeks g ON g.id = pg.fpl_gameweek_id
		WHERE g.season = $1
		GROUP BY pg.player_id
	`
	rows, err := db.Query(query, season)
	if err != nil {
		return nil, fmt.Errorf("loading actual FPL points: %w", err)
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var playerID, total int
		if err := rows.Scan(&playerID, &total); err != nil {
			return nil, err
		}
		result[playerID] = total
	}
	return result, rows.Err()
}

// CompetitionTier categorises competition quality for minutes adjustment.
type CompetitionTier int

const (
	TierPL         CompetitionTier = iota // English Premier League
	TierTop5                              // Bundesliga, La Liga, Serie A, Ligue 1
	TierChampionship                      // English Championship
	TierOther                             // Everything else
)

// LoadCompetitionTiers loads competition ID -> tier mapping.
func LoadCompetitionTiers(db *sql.DB) (map[int]CompetitionTier, error) {
	query := `
		SELECT c.id, c.name, co.name as country
		FROM competitions c
		JOIN countries co ON co.id = c.country_id
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int]CompetitionTier)
	for rows.Next() {
		var id int
		var name, country string
		if err := rows.Scan(&id, &name, &country); err != nil {
			return nil, err
		}

		switch {
		case id == englishPLCompetitionID:
			result[id] = TierPL
		case name == "Championship" && country == "England":
			result[id] = TierChampionship
		case name == "Bundesliga" || name == "Primera Division" ||
			name == "Serie A" || name == "Ligue 1" ||
			name == "Primeira Liga" || name == "Eredivisie":
			result[id] = TierTop5
		default:
			result[id] = TierOther
		}
	}
	return result, rows.Err()
}

// LoadActualCSRates loads historical clean sheet rates per team per season
// from actual PL results.
func LoadActualCSRates(db *sql.DB, beforeSeason int) (map[int]map[int]float64, error) {
	query := `
		SELECT t.id as team_id, f.season,
		       count(*) FILTER (WHERE
		         (f.home_team_id = t.id AND f.away_team_goals = 0) OR
		         (f.away_team_id = t.id AND f.home_team_goals = 0)
		       )::float / count(*)::float as cs_rate
		FROM fixtures f
		JOIN teams t ON t.id = f.home_team_id OR t.id = f.away_team_id
		WHERE f.competition_id = $1 AND f.season < $2
		  AND f.home_team_goals IS NOT NULL
		GROUP BY t.id, f.season
	`
	rows, err := db.Query(query, englishPLCompetitionID, beforeSeason)
	if err != nil {
		return nil, fmt.Errorf("loading CS rates: %w", err)
	}
	defer rows.Close()

	// team_id -> season -> cs_rate
	result := make(map[int]map[int]float64)
	for rows.Next() {
		var teamID, season int
		var csRate float64
		if err := rows.Scan(&teamID, &season, &csRate); err != nil {
			return nil, err
		}
		if result[teamID] == nil {
			result[teamID] = make(map[int]float64)
		}
		result[teamID][season] = csRate
	}
	return result, rows.Err()
}

// LoadPositionNames loads position ID -> name mapping.
func LoadPositionNames(db *sql.DB) (map[int]string, error) {
	rows, err := db.Query("SELECT id, name FROM positions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int]string)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		result[id] = name
	}
	return result, rows.Err()
}

// LoadPlayerPrices returns each player's starting price for a season.
// It first tries fpl_season_info.starting_price, then falls back to
// GW1 values from fpl_players_gameweeks. Team assignment is handled
// separately via LoadPlayerTeams (players_teams table).
func LoadPlayerPrices(db *sql.DB, season int) (map[int]PlayerPrice, error) {
	result := make(map[int]PlayerPrice)

	// Primary source: starting_price in fpl_season_info.
	rows, err := db.Query(`
		SELECT fsi.player_id, fsi.starting_price
		FROM fpl_season_info fsi
		WHERE fsi.season = $1 AND fsi.starting_price IS NOT NULL
	`, season)
	if err != nil {
		return nil, fmt.Errorf("loading starting prices: %w", err)
	}
	for rows.Next() {
		var pid int
		var price float64
		if err := rows.Scan(&pid, &price); err != nil {
			rows.Close()
			return nil, err
		}
		result[pid] = PlayerPrice{Price: price}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(result) > 0 {
		return result, nil
	}

	// Fallback: GW1 values from FPL gameweek data (no team_id available).
	rows, err = db.Query(`
		SELECT pg.player_id, pg.value
		FROM fpl_players_gameweeks pg
		JOIN fpl_gameweeks g ON g.id = pg.fpl_gameweek_id
		WHERE g.season = $1 AND g.gameweek = 1 AND pg.value IS NOT NULL
	`, season)
	if err != nil {
		return nil, fmt.Errorf("loading gameweek 1 prices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var price float64
		if err := rows.Scan(&pid, &price); err != nil {
			return nil, err
		}
		result[pid] = PlayerPrice{Price: price}
	}
	return result, rows.Err()
}

// LoadFixturesByGameweek returns the first numGWs fixtures for each PL
// team in a season, using the fixtures_fpl_gameweeks mapping table.
func LoadFixturesByGameweek(db *sql.DB, season, numGWs int) (map[int][]TeamFixture, error) {
	rows, err := db.Query(`
		SELECT f.home_team_id, f.away_team_id, g.gameweek
		FROM fixtures_fpl_gameweeks ffg
		JOIN fixtures f ON f.id = ffg.fixture_id
		JOIN fpl_gameweeks g ON g.id = ffg.gameweek_id
		WHERE f.competition_id = $1 AND g.season = $2 AND g.gameweek <= $3
		ORDER BY g.gameweek, f.fixture_date
	`, englishPLCompetitionID, season, numGWs)
	if err != nil {
		return nil, fmt.Errorf("loading fixtures by gameweek: %w", err)
	}
	defer rows.Close()

	result := make(map[int][]TeamFixture)
	for rows.Next() {
		var homeID, awayID, gw int
		if err := rows.Scan(&homeID, &awayID, &gw); err != nil {
			return nil, err
		}
		result[homeID] = append(result[homeID], TeamFixture{
			Gameweek:   gw,
			OpponentID: awayID,
			IsHome:     true,
		})
		result[awayID] = append(result[awayID], TeamFixture{
			Gameweek:   gw,
			OpponentID: homeID,
			IsHome:     false,
		})
	}
	return result, rows.Err()
}
