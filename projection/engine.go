package projection

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

const (
	// Season weights for per-90 rate calculations.
	// Index 0 = most recent season, etc.
	seasonWeight0 = 0.50
	seasonWeight1 = 0.30
	seasonWeight2 = 0.20

	// Minimum minutes for reliable per-90 rates.
	minMinutesForRates = 450 // ~5 full matches

	// Regression strength: how much to pull towards positional mean.
	// Higher = more regression. Applied as: (playerRate * minutes + meanRate * regressionMinutes) / (minutes + regressionMinutes)
	regressionMinutes = 1800.0 // ~20 full matches — strong regression

	// Max realistic minutes in a PL season.
	maxSeasonMinutes = 3420.0 // 38 * 90

	// H2H consistency penalty weight.
	consistencyLambda = 2.0

	// DEFCON: probability of hitting threshold per match, by detailed position.
	defconProbCB  = 0.35 // centre-backs hit it often
	defconProbDM  = 0.25 // defensive midfielders
	defconProbFB  = 0.15 // full-backs
	defconProbMid = 0.08 // other midfielders
	defconProbFwd = 0.05 // forwards (very rare)
	defconProbGK  = 0.10 // goalkeepers
)

var seasonWeights = []float64{seasonWeight0, seasonWeight1, seasonWeight2}

// Engine runs projections for a set of players.
type Engine struct {
	DB            *sql.DB
	TargetSeason  int
	LeagueSize    int
	TeamStrengths map[int]*TeamStrength
	PositionNames map[int]string
	CompTiers     map[int]CompetitionTier
	ActualCSRates map[int]map[int]float64 // team -> season -> cs_rate

	// If true, build player pool from fixture data instead of fpl_season_info.
	Backtest bool

	// Positional mean rates (computed from the player pool).
	posMeanXGPer90  map[FPLPosition]float64
	posMeanXAPer90  map[FPLPosition]float64
	posMeanBPSPer90 map[FPLPosition]float64

	// League average offensive rating (for team context adjustment).
	leagueAvgOffRating float64
}

// NewEngine creates a projection engine for the given target season.
func NewEngine(db *sql.DB, targetSeason int, leagueSize int) *Engine {
	return &Engine{
		DB:           db,
		TargetSeason: targetSeason,
		LeagueSize:   leagueSize,
	}
}

// Run executes the full projection pipeline and returns ranked output.
func (e *Engine) Run() (*ProjectionOutput, error) {
	// 1. Load team strengths.
	var err error
	e.TeamStrengths, err = LoadTeamStrengths(e.DB, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading team strengths: %w", err)
	}

	// Compute league average offensive rating.
	var totalOff float64
	var teamCount int
	for _, ts := range e.TeamStrengths {
		totalOff += ts.OffensiveRating
		teamCount++
	}
	if teamCount > 0 {
		e.leagueAvgOffRating = totalOff / float64(teamCount)
	}

	// 2. Load position names, competition tiers, and actual CS rates.
	e.PositionNames, err = LoadPositionNames(e.DB)
	if err != nil {
		return nil, fmt.Errorf("loading positions: %w", err)
	}

	e.CompTiers, err = LoadCompetitionTiers(e.DB)
	if err != nil {
		return nil, fmt.Errorf("loading competition tiers: %w", err)
	}

	e.ActualCSRates, err = LoadActualCSRates(e.DB, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading positions: %w", err)
	}

	// 3. Load active players (try active flag first, fall back to all).
	players, err := LoadActivePlayers(e.DB, e.TargetSeason)
	if err != nil || len(players) == 0 {
		players, err = LoadAllFPLPlayers(e.DB, e.TargetSeason)
		if err != nil {
			return nil, fmt.Errorf("loading players: %w", err)
		}
	}

	if len(players) == 0 {
		return nil, fmt.Errorf("no players found for season %d", e.TargetSeason)
	}

	// 4. Get player IDs for batch loading.
	playerIDs := make([]int, len(players))
	for i, p := range players {
		playerIDs[i] = p.ID
	}

	// 5. Load player fixtures (historical match data).
	fixtures, err := LoadPlayerFixtures(e.DB, playerIDs, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading fixtures: %w", err)
	}

	// 6. Load FPL gameweek history.
	fplGWs, err := LoadFPLGameweeks(e.DB, playerIDs, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading FPL gameweeks: %w", err)
	}

	// 7. Load player teams.
	playerTeams, err := LoadPlayerTeams(e.DB, playerIDs, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading player teams: %w", err)
	}

	// 8. Compute per-90 rates for each player and enrich.
	for i := range players {
		p := &players[i]
		if team, ok := playerTeams[p.ID]; ok {
			p.TeamID = team.TeamID
			p.TeamName = team.TeamName
			p.IsOnPLTeam = team.IsPL
		}
		e.computeRates(p, fixtures[p.ID])
		e.enrichFPLHistory(p, fplGWs[p.ID])
	}

	// 9. Compute positional mean rates for regression.
	e.computePositionalMeans(players, fixtures)

	// 10. Project each player.
	projections := make([]PlayerProjection, 0, len(players))
	for i := range players {
		proj := e.projectPlayer(&players[i])
		projections = append(projections, proj)
	}

	// 11. Sort by projected points descending.
	sort.Slice(projections, func(i, j int) bool {
		return projections[i].ProjectedPoints > projections[j].ProjectedPoints
	})

	// 12. Compute VORP.
	e.computeVORP(projections)

	// 13. Compute H2H adjusted scores.
	e.computeH2HAdjusted(projections)

	// 14. Re-sort by VORP.
	sort.Slice(projections, func(i, j int) bool {
		return projections[i].VORP > projections[j].VORP
	})

	return &ProjectionOutput{
		Season:     e.TargetSeason,
		LeagueSize: e.LeagueSize,
		Players:    projections,
	}, nil
}

// computeRates calculates per-90 rates from historical fixture data.
// Uses all competition data for per-90 rates (more data = more stable)
// but tracks PL-only minutes separately for minutes projection.
func (e *Engine) computeRates(p *Player, fixtures []playerFixtureRow) {
	if len(fixtures) == 0 {
		return
	}

	// Group by season, tracking all-competition and PL-only separately.
	type seasonStats struct {
		// All competitions.
		minutes  int
		goals    int
		assists  int
		yellows  int
		reds     int
		xg       float64
		xa       float64
		npxg     float64
		xgCount  int
		// PL only.
		plMinutes  int
		plGoals    int
		plAssists  int
		plYellows  int
		plReds     int
		plXG       float64
		plXA       float64
		plNPXG     float64
		plXGCount  int
		// Competition breakdown for minutes.
		compMinutes map[int]int // competition_id -> minutes
	}

	seasonMap := make(map[int]*seasonStats)
	posCount := make(map[string]int)

	for _, f := range fixtures {
		if seasonMap[f.Season] == nil {
			seasonMap[f.Season] = &seasonStats{compMinutes: make(map[int]int)}
		}
		s := seasonMap[f.Season]
		s.compMinutes[f.CompetitionID] += f.Minutes
		s.minutes += f.Minutes
		s.goals += f.Goals
		s.assists += f.Assists
		if f.YellowCard {
			s.yellows++
		}
		if f.RedCard {
			s.reds++
		}
		if f.XG.Valid {
			s.xg += f.XG.Float64
			s.xa += f.XA.Float64
			s.npxg += f.NPXG.Float64
			s.xgCount++
		}
		if f.IsPL {
			s.plMinutes += f.Minutes
			s.plGoals += f.Goals
			s.plAssists += f.Assists
			if f.YellowCard {
				s.plYellows++
			}
			if f.RedCard {
				s.plReds++
			}
			if f.XG.Valid {
				s.plXG += f.XG.Float64
				s.plXA += f.XA.Float64
				s.plNPXG += f.NPXG.Float64
				s.plXGCount++
			}
		}
		if f.PositionID.Valid {
			posName := e.PositionNames[int(f.PositionID.Int64)]
			posCount[posName]++
		}
	}

	// Find primary detailed position.
	var maxPosCount int
	for pos, count := range posCount {
		if count > maxPosCount {
			maxPosCount = count
			p.PrimaryDetailedPos = pos
		}
	}

	// Sort seasons descending for weighting.
	seasons := make([]int, 0, len(seasonMap))
	for s := range seasonMap {
		seasons = append(seasons, s)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(seasons)))

	// Compute weighted per-90 rates. Prefer PL-only data when the player
	// has enough PL minutes in a season (>= 450); fall back to all-comp.
	var totalWeight float64
	var wXG, wXA, wGoals, wAssists, wYellows, wReds, wNPXG float64
	var weightedRateMinutes float64

	for i, s := range seasons {
		if i >= len(seasonWeights) {
			break
		}
		stats := seasonMap[s]
		if stats.minutes < 90 {
			continue
		}

		w := seasonWeights[i]
		totalWeight += w

		// Use PL data if sufficient, otherwise all-comp.
		useMinutes := stats.minutes
		useGoals := stats.goals
		useAssists := stats.assists
		useYellows := stats.yellows
		useReds := stats.reds
		useXG := stats.xg
		useXA := stats.xa
		useNPXG := stats.npxg
		useXGCount := stats.xgCount

		if stats.plMinutes >= 450 {
			useMinutes = stats.plMinutes
			useGoals = stats.plGoals
			useAssists = stats.plAssists
			useYellows = stats.plYellows
			useReds = stats.plReds
			if stats.plXGCount > 0 {
				useXG = stats.plXG
				useXA = stats.plXA
				useNPXG = stats.plNPXG
				useXGCount = stats.plXGCount
			}
		}

		weightedRateMinutes += float64(useMinutes)
		per90 := float64(useMinutes) / 90.0

		wGoals += w * (float64(useGoals) / per90)
		wAssists += w * (float64(useAssists) / per90)
		wYellows += w * (float64(useYellows) / per90)
		wReds += w * (float64(useReds) / per90)

		if useXGCount > 0 {
			wXG += w * (useXG / per90)
			wXA += w * (useXA / per90)
			wNPXG += w * (useNPXG / per90)
		} else {
			wXG += w * (float64(useGoals) / per90)
			wXA += w * (float64(useAssists) / per90)
		}
	}

	if totalWeight > 0 {
		p.XGPer90 = wXG / totalWeight
		p.XAPer90 = wXA / totalWeight
		p.GoalsPer90 = wGoals / totalWeight
		p.AssistsPer90 = wAssists / totalWeight
		p.YellowsPer90 = wYellows / totalWeight
		p.RedsPer90 = wReds / totalWeight
		p.NPXGPer90 = wNPXG / totalWeight
	}

	// Track weighted minutes used for regression (only from seasons
	// that contributed to the rate calculation).
	p.WeightedRateMinutes = weightedRateMinutes

	// Check if player had any PL minutes in recent seasons.
	var hadPLMinutes bool
	for i, s := range seasons {
		if i >= len(seasonWeights) {
			break
		}
		if seasonMap[s].plMinutes > 0 {
			hadPLMinutes = true
			break
		}
	}
	p.IsTransferIn = !hadPLMinutes && p.IsOnPLTeam

	// Build minutes per season for projection. Use PL minutes if available,
	// otherwise use domestic league minutes with a competition-tier discount.
	var rawMinutesTotal float64
	for i, s := range seasons {
		if i >= len(seasonWeights) {
			break
		}
		stats := seasonMap[s]
		rawMinutesTotal += float64(stats.minutes)
		projMin := stats.plMinutes
		if projMin == 0 {
			// Player wasn't in PL this season. Find their best
			// domestic league tier and apply appropriate discount.
			bestTier := TierOther
			var domesticMin int
			for compID, min := range stats.compMinutes {
				tier := e.CompTiers[compID]
				if tier < bestTier {
					bestTier = tier
				}
				// Sum non-European-cup minutes for domestic estimate.
				if tier != TierOther || compID < 200 {
					domesticMin += min
				}
			}
			if domesticMin == 0 {
				domesticMin = stats.minutes
			}

			var discount float64
			switch bestTier {
			case TierTop5:
				discount = 0.80 // top-5 league starters translate well
			case TierChampionship:
				discount = 0.65 // Championship is a step below
			default:
				discount = 0.50 // other leagues
			}
			projMin = int(float64(domesticMin) * discount)
		}
		// Cap individual season at PL max.
		if projMin > int(maxSeasonMinutes) {
			projMin = int(maxSeasonMinutes)
		}
		p.MinutesPerSeason = append(p.MinutesPerSeason, SeasonMinutes{
			Season:  s,
			Minutes: projMin,
			Weight:  seasonWeights[i],
		})
	}
	p.RawMinutesRecent = rawMinutesTotal
}

// enrichFPLHistory adds FPL-specific data (BPS, gameweek points for consistency).
func (e *Engine) enrichFPLHistory(p *Player, gws []fplGWRow) {
	if len(gws) == 0 {
		return
	}
	p.HasFPLHistory = true

	// BPS per 90: we need to correlate with minutes, but FPL GW data
	// doesn't have minutes. Use total BPS / estimated appearances * 90.
	// Simple approach: sum BPS, count non-zero GWs, assume ~70 min avg.
	var totalBPS int
	var nonZeroGWs int
	for _, gw := range gws {
		if gw.TotalPoints > 0 { // played
			totalBPS += gw.BPS
			nonZeroGWs++
		}
		p.HistoricGWPoints = append(p.HistoricGWPoints, float64(gw.TotalPoints))
	}

	if nonZeroGWs > 0 {
		// Approximate: assume average 70 min per appearance.
		estimatedMinutes := float64(nonZeroGWs) * 70.0
		p.BPSPer90 = float64(totalBPS) / (estimatedMinutes / 90.0)
	}
}

// computePositionalMeans calculates average per-90 rates by FPL position.
func (e *Engine) computePositionalMeans(players []Player, fixtures map[int][]playerFixtureRow) {
	e.posMeanXGPer90 = make(map[FPLPosition]float64)
	e.posMeanXAPer90 = make(map[FPLPosition]float64)
	e.posMeanBPSPer90 = make(map[FPLPosition]float64)

	type posAcc struct {
		xg, xa, bps float64
		count       int
	}
	acc := make(map[FPLPosition]*posAcc)

	for _, p := range players {
		if p.XGPer90 == 0 && p.GoalsPer90 == 0 {
			continue
		}
		if acc[p.Position] == nil {
			acc[p.Position] = &posAcc{}
		}
		a := acc[p.Position]
		// Only count players with meaningful data.
		totalMin := 0
		for _, sm := range p.MinutesPerSeason {
			totalMin += sm.Minutes
		}
		if totalMin < minMinutesForRates {
			continue
		}
		a.xg += p.XGPer90
		a.xa += p.XAPer90
		a.bps += p.BPSPer90
		a.count++
	}

	for pos, a := range acc {
		if a.count > 0 {
			e.posMeanXGPer90[pos] = a.xg / float64(a.count)
			e.posMeanXAPer90[pos] = a.xa / float64(a.count)
			e.posMeanBPSPer90[pos] = a.bps / float64(a.count)
		}
	}
}

// projectPlayer generates a full projection for a single player.
func (e *Engine) projectPlayer(p *Player) PlayerProjection {
	proj := PlayerProjection{
		PlayerID:  p.ID,
		FirstName: p.FirstName,
		LastName:  p.LastName,
		TeamName:  p.TeamName,
		Position:  p.Position.String(),
	}

	// --- Minutes projection ---
	projMinutes := e.projectMinutes(p)
	proj.ProjectedMinutes = projMinutes

	if projMinutes < 45 {
		// Player unlikely to play — minimal projection.
		return proj
	}

	appearances90 := projMinutes / 90.0
	// Estimate number of matches where they appear.
	matchesPlayed := math.Min(projMinutes/60.0, 38.0)

	// --- Regressed per-90 rates ---
	xgPer90 := e.regressRate(p.XGPer90, e.posMeanXGPer90[p.Position], p.WeightedRateMinutes)
	xaPer90 := e.regressRate(p.XAPer90, e.posMeanXAPer90[p.Position], p.WeightedRateMinutes)
	yellowsPer90 := p.YellowsPer90
	redsPer90 := p.RedsPer90

	// --- Team context adjustment ---
	teamAdj := e.teamContextMultiplier(p.TeamID)
	xgPer90 *= teamAdj
	xaPer90 *= teamAdj

	// --- Goals and assists ---
	proj.ProjectedGoals = xgPer90 * appearances90
	proj.ProjectedAssists = xaPer90 * appearances90

	// --- Clean sheets ---
	csProb := e.teamCSProbability(p.TeamID)
	proj.ProjectedCleanSheets = csProb * matchesPlayed

	// --- Bonus points ---
	bpsPer90 := p.BPSPer90
	if !p.HasFPLHistory || bpsPer90 == 0 {
		bpsPer90 = e.posMeanBPSPer90[p.Position]
	}
	// Convert BPS per 90 to bonus points. Calibrated from 2025 data:
	// BPS 10/app -> 0.17 bonus/match, BPS 15 -> 0.37, BPS 20 -> 0.70
	// Capped since bonus is awarded to top-3 BPS in each match, so
	// even the best players can't exceed ~0.75/match over a season.
	bonusPerMatch := math.Max(0, 0.053*bpsPer90-0.36)
	bonusPerMatch = math.Min(bonusPerMatch, 0.75)
	// GKs accumulate high BPS from saves but rarely win the bonus
	// because they compete with outfield scorers.
	if p.Position == Goalkeeper {
		bonusPerMatch = math.Min(bonusPerMatch, 0.35)
	}
	proj.ProjectedBonus = bonusPerMatch * matchesPlayed

	// --- Cards ---
	proj.ProjectedYellows = yellowsPer90 * appearances90
	proj.ProjectedReds = redsPer90 * appearances90

	// --- DEFCON ---
	defconProb := e.defconProbability(p)
	proj.ProjectedDEFCON = defconProb * matchesPlayed

	// --- Convert to FPL points ---

	// Appearance points: 2 for 60+ min, 1 for <60 min.
	// For MID and DEF, use position-specific 60+ minute rates
	// calibrated from PL data, scaled by this player's projected
	// avg minutes relative to the position norm.
	// GK and FWD keep the original avg-min-based logic.
	var fullMatchFrac float64
	switch p.Position {
	case Defender:
		fullMatchFrac = 0.80
		if projMinutes > 0 && matchesPlayed > 0 {
			ratio := (projMinutes / matchesPlayed) / 77.0
			fullMatchFrac = math.Min(0.98, fullMatchFrac*ratio)
		}
	case Midfielder:
		fullMatchFrac = 0.64
		if projMinutes > 0 && matchesPlayed > 0 {
			ratio := (projMinutes / matchesPlayed) / 67.0
			fullMatchFrac = math.Min(0.98, fullMatchFrac*ratio)
		}
	default:
		fullMatchFrac = 0.75
		if projMinutes > 0 && matchesPlayed > 0 {
			avgMinPerMatch := projMinutes / matchesPlayed
			if avgMinPerMatch >= 75 {
				fullMatchFrac = 0.9
			} else if avgMinPerMatch >= 60 {
				fullMatchFrac = 0.8
			} else {
				fullMatchFrac = 0.4
			}
		}
	}
	proj.AppearancePoints = matchesPlayed * (fullMatchFrac*2.0 + (1.0-fullMatchFrac)*1.0)

	proj.GoalPoints = proj.ProjectedGoals * p.Position.GoalPoints()
	proj.AssistPoints = proj.ProjectedAssists * 3.0
	proj.CleanSheetPoints = proj.ProjectedCleanSheets * p.Position.CleanSheetPoints()
	proj.BonusPoints = proj.ProjectedBonus

	proj.CardPoints = -(proj.ProjectedYellows*1.0 + proj.ProjectedReds*3.0)

	// Goals conceded per match (used for GC penalty and regression).
	var gcPerMatch float64
	if ts, ok := e.TeamStrengths[p.TeamID]; ok {
		gcPerMatch = ts.GoalsConcededPerMatch
	} else {
		gcPerMatch = 1.3
	}

	// Goals conceded penalty: GK/DEF lose 1 pt per 2 goals conceded.
	if p.Position == Goalkeeper || p.Position == Defender {
		totalGC := gcPerMatch * matchesPlayed
		proj.GoalsConcededPen = -(totalGC / 2.0)
	}

	proj.DEFCONPoints = proj.ProjectedDEFCON * 2.0

	// Save points: GK only, 1 pt per 3 saves.
	// Calibrated from actual FPL data: starting GKs average ~2.3 FPL
	// saves per match, varying by team defensive quality.
	if p.Position == Goalkeeper {
		var savesPerMatch float64
		if ts, ok := e.TeamStrengths[p.TeamID]; ok {
			// Better defensive teams face fewer shots → fewer saves.
			// Bad teams face more shots → more saves.
			// Calibration: savesPerMatch ≈ 1.0 + 1.0 * xGA
			savesPerMatch = 1.0 + 1.0*ts.GoalsConcededPerMatch
			if savesPerMatch < 1.5 {
				savesPerMatch = 1.5
			}
		} else {
			savesPerMatch = 2.3
		}
		proj.SavePoints = (savesPerMatch * matchesPlayed) / 3.0
	}

	manualTotal := proj.AppearancePoints +
		proj.GoalPoints +
		proj.AssistPoints +
		proj.CleanSheetPoints +
		proj.SavePoints +
		proj.BonusPoints +
		proj.CardPoints +
		proj.GoalsConcededPen +
		proj.DEFCONPoints

	// Regression model: FPL_per90 from per-90 stats + team context.
	// Coefficients learned from 2020-2024 PL data via weighted OLS.
	var fplPer90 float64
	switch p.Position {
	case Goalkeeper:
		fplPer90 = -1.2808 + 9.6820*xgPer90 + 4.0947*xaPer90 + 8.4996*yellowsPer90 +
			11.2793*csProb + 1.4993*gcPerMatch
	case Defender:
		fplPer90 = 0.6733 + 6.0066*xgPer90 + 4.8363*xaPer90 + 1.2720*yellowsPer90 +
			7.5786*csProb + 0.0011*gcPerMatch
	case Midfielder:
		fplPer90 = 0.8004 + 6.6982*xgPer90 + 3.5295*xaPer90 - 0.0204*yellowsPer90 +
			3.6206*csProb + 0.3866*gcPerMatch
	case Forward:
		fplPer90 = 3.0536 + 3.7699*xgPer90 + 5.1795*xaPer90 + 1.6508*yellowsPer90 -
			0.6691*csProb - 0.2469*gcPerMatch
	}
	regressionTotal := fplPer90 * appearances90

	// Blend: 60% manual (well-calibrated totals) + 40% regression
	// (better learned bonus/DEFCON/interaction effects).
	proj.ProjectedPoints = 0.60*manualTotal + 0.40*regressionTotal

	// --- Consistency metrics ---
	if len(p.HistoricGWPoints) > 0 {
		mean, stddev := meanStdDev(p.HistoricGWPoints)
		proj.Consistency = stddev
		proj.Floor = percentile(p.HistoricGWPoints, 0.10)
		_ = mean
	} else {
		// Estimate from position.
		switch p.Position {
		case Forward:
			proj.Consistency = 3.5
		case Midfielder:
			proj.Consistency = 3.0
		case Defender:
			proj.Consistency = 3.2
		case Goalkeeper:
			proj.Consistency = 3.0
		}
		proj.Floor = 1.0
	}

	return proj
}

// projectMinutes estimates total PL season minutes for a player.
func (e *Engine) projectMinutes(p *Player) float64 {
	if len(p.MinutesPerSeason) == 0 {
		if p.IsOnPLTeam {
			if p.IsTransferIn {
				return 1200
			}
			return 500
		}
		return 0
	}

	// Weighted average of recent season minutes (already PL-adjusted
	// in computeRates).
	var totalWeight, weightedMinutes float64
	for _, sm := range p.MinutesPerSeason {
		if sm.Weight == 0 {
			continue
		}
		totalWeight += sm.Weight
		weightedMinutes += sm.Weight * float64(sm.Minutes)
	}

	if totalWeight == 0 {
		if p.IsOnPLTeam {
			if p.IsTransferIn {
				return 1200
			}
			return 500
		}
		return 0
	}

	weightedAvg := weightedMinutes / totalWeight

	// Trend adjustment: if the most recent season has significantly
	// more minutes than the weighted average, the player is on an
	// upward trajectory (new starter, moved to a team where they
	// play more). Use the higher of the weighted average and 85% of
	// the most recent season to avoid punishing improving players.
	mostRecent := float64(p.MinutesPerSeason[0].Minutes)
	projected := math.Max(weightedAvg, mostRecent*0.85)

	// Transfer-in boost: if a player had no PL minutes in recent
	// seasons but is now at a PL team with significant raw minutes
	// from other leagues, they were bought to play.
	if p.IsTransferIn && p.RawMinutesRecent > 3000 {
		var transferFloor float64
		if p.RawMinutesRecent > 6000 {
			transferFloor = 2200
		} else {
			transferFloor = 1800
		}
		projected = math.Max(projected, transferFloor)
	}

	// Cap at realistic PL maximum (38 matches * 90 min).
	if projected > maxSeasonMinutes {
		projected = maxSeasonMinutes
	}

	// If player is not currently on a PL team, discount further.
	if !p.IsOnPLTeam {
		projected *= 0.5
	}

	return projected
}

// regressRate pulls a player's rate towards the positional mean based on
// sample size.
func (e *Engine) regressRate(playerRate, posMean, minutes float64) float64 {
	if minutes == 0 && playerRate == 0 {
		return posMean
	}
	// Bayesian-style regression: weight player data vs prior.
	return (playerRate*minutes + posMean*regressionMinutes) / (minutes + regressionMinutes)
}

// teamContextMultiplier returns a scaling factor for attacking rates
// based on the player's team strength vs league average.
func (e *Engine) teamContextMultiplier(teamID int) float64 {
	ts, ok := e.TeamStrengths[teamID]
	if !ok || e.leagueAvgOffRating == 0 {
		return 1.0
	}
	// Scale by ratio of team's offensive rating to league average.
	// Dampen the effect: use sqrt to avoid extreme adjustments.
	ratio := ts.OffensiveRating / e.leagueAvgOffRating
	return math.Sqrt(ratio)
}

// teamCSProbability returns the estimated clean sheet probability per match
// for a team, blending actual historical CS rates with the Poisson model.
func (e *Engine) teamCSProbability(teamID int) float64 {
	// Try actual CS rates first (weighted by recency).
	if rates, ok := e.ActualCSRates[teamID]; ok && len(rates) > 0 {
		seasons := make([]int, 0, len(rates))
		for s := range rates {
			seasons = append(seasons, s)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(seasons)))

		var totalWeight, weightedCS float64
		for i, s := range seasons {
			if i >= len(seasonWeights) {
				break
			}
			w := seasonWeights[i]
			totalWeight += w
			weightedCS += w * rates[s]
		}
		if totalWeight > 0 {
			actualRate := weightedCS / totalWeight
			// Blend with Poisson model (70% actual, 30% Poisson) to
			// handle promoted/changed teams.
			if ts, ok := e.TeamStrengths[teamID]; ok {
				return 0.7*actualRate + 0.3*ts.CleanSheetProb
			}
			return actualRate
		}
	}

	// Fall back to Poisson model.
	if ts, ok := e.TeamStrengths[teamID]; ok {
		return ts.CleanSheetProb
	}
	return 0.22 // league average
}

// defconProbability estimates the per-match probability of a player
// hitting the DEFCON threshold.
func (e *Engine) defconProbability(p *Player) float64 {
	switch p.PrimaryDetailedPos {
	case "DC":
		return defconProbCB
	case "DMC":
		return defconProbDM
	case "DL", "DR":
		return defconProbFB
	case "GK":
		return defconProbGK
	case "MC", "ML", "MR", "DML", "DMR":
		return defconProbMid
	case "AML", "AMC", "AMR":
		return defconProbMid * 0.5
	case "FW", "FWL", "FWR":
		return defconProbFwd
	default:
		// Fall back based on FPL position.
		switch p.Position {
		case Goalkeeper:
			return defconProbGK
		case Defender:
			return defconProbFB
		case Midfielder:
			return defconProbMid
		case Forward:
			return defconProbFwd
		default:
			return 0
		}
	}
}

// computeVORP calculates Value Over Replacement Player for each projection.
func (e *Engine) computeVORP(projections []PlayerProjection) {
	// Count how many of each position are drafted in an 8-team league.
	// 2 GK, 5 DEF, 5 MID, 3 FWD per team.
	draftedPerPos := map[string]int{
		"GK":  e.LeagueSize * 2,
		"DEF": e.LeagueSize * 5,
		"MID": e.LeagueSize * 5,
		"FWD": e.LeagueSize * 3,
	}

	// Sort by projected points within each position to find replacement level.
	posPlayers := make(map[string][]float64)
	for _, p := range projections {
		posPlayers[p.Position] = append(posPlayers[p.Position], p.ProjectedPoints)
	}
	for pos := range posPlayers {
		sort.Sort(sort.Reverse(sort.Float64Slice(posPlayers[pos])))
	}

	// Replacement level = points of the (N+1)th player at each position.
	replacementLevel := make(map[string]float64)
	for pos, drafted := range draftedPerPos {
		pts := posPlayers[pos]
		if drafted < len(pts) {
			replacementLevel[pos] = pts[drafted]
		} else if len(pts) > 0 {
			replacementLevel[pos] = pts[len(pts)-1]
		}
	}

	for i := range projections {
		projections[i].VORP = projections[i].ProjectedPoints - replacementLevel[projections[i].Position]
	}
}

// computeH2HAdjusted adjusts projected points for H2H consistency.
func (e *Engine) computeH2HAdjusted(projections []PlayerProjection) {
	for i := range projections {
		projections[i].H2HAdjustedPts = projections[i].ProjectedPoints - consistencyLambda*projections[i].Consistency
	}
}

// --- Utility functions ---

func meanStdDev(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))

	var sumSqDiff float64
	for _, v := range vals {
		diff := v - mean
		sumSqDiff += diff * diff
	}
	variance := sumSqDiff / float64(len(vals))
	return mean, math.Sqrt(variance)
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)

	idx := p * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
