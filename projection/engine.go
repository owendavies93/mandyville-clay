package projection

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	// Season weights for per-90 rate calculations.
	// Index 0 = most recent season, etc.
	seasonWeight0 = 0.70
	seasonWeight1 = 0.30

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

	// In-season Bayesian blending.
	// Current-season per-90 rates get weight m/(m+k) where m is
	// current-season minutes and k is the shrinkage constant below.
	rateShrinkageMinutes = 900.0

	// Current-season minutes per fixture get weight n/(n+K) where n is
	// the number of current-season appearances.
	minutesShrinkageAppearances = 5.0

	// Team strengths blend current-season form with the prior using the
	// same scheme, keyed on current-season matches played.
	teamShrinkageMatches = 10.0

	// Expected-return decay for injuries and suspensions the game gives
	// no return date for. The chance of being back by the nth fixture
	// after the current one is 1-exp(-n/tau), so a player is always out
	// for the next match and recovers from there. FPL reports "Unknown
	// return date" for everything from a fortnight's knock to a
	// cruciate, hence a tau long enough not to rush stars back.
	injuryReturnTau = 3.0

	// Suspensions are far more predictable: a one-match ban is the norm,
	// with three for a red card.
	suspensionReturnTau = 1.0

	// EngineVersion is recorded against persisted projection runs so old
	// snapshots can be traced back to the model that produced them.
	EngineVersion = "2.0-fixture-level"
)

var seasonWeights = []float64{seasonWeight0, seasonWeight1}

// Engine runs projections for a set of players.
type Engine struct {
	DB            *sql.DB
	TargetSeason  int
	LeagueSize    int
	Rules         ScoringRules
	TeamStrengths map[int]*TeamStrength
	PositionNames map[int]string
	CompTiers     map[int]CompetitionTier
	ActualCSRates map[int]map[int]float64 // team -> season -> cs_rate

	// If true, build player pool from fixture data instead of fpl_season_info.
	Backtest bool

	// AsOfGameweek > 0 means the engine projects the remaining fixtures
	// from that gameweek onward, blending current-season data observed
	// before that gameweek's deadline. 0 means a pre-season projection.
	AsOfGameweek int

	// Internal state populated per run.
	fixturesByTeam map[int][]TeamFixture
	availability   map[int]Availability
	prior          map[int]PriorRates

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
		Rules:        ClassicRules,
	}
}

// Run executes a pre-season projection: prior-season data only, projected
// over the full 38-gameweek schedule.
func (e *Engine) Run() (*ProjectionOutput, error) {
	players, err := e.loadPrior()
	if err != nil {
		return nil, err
	}

	e.fixturesByTeam, err = LoadFixturesWindow(e.DB, e.TargetSeason, 1, 38)
	if err != nil {
		return nil, fmt.Errorf("loading fixtures: %w", err)
	}

	playerIDs := make([]int, len(players))
	for i := range players {
		playerIDs[i] = players[i].ID
	}
	// Availability is best-effort: the cron may not have run yet, or the
	// season may predate availability capture.
	e.availability, _ = LoadPlayerAvailability(e.DB, e.TargetSeason, playerIDs)

	e.prior = make(map[int]PriorRates, len(players))
	projections := make([]PlayerProjection, 0, len(players))
	for i := range players {
		p := &players[i]
		in := e.priorInputs(p)
		e.prior[p.ID] = in
		projections = append(projections, e.projectPlayer(p, in))
	}

	return e.finalize(projections), nil
}

// PriorRates returns the per-player pre-season prior rates computed during
// the last run, keyed by player ID. Only populated by Run (pre-season).
func (e *Engine) PriorRates() map[int]PriorRates {
	return e.prior
}

// RunInSeason projects the remaining fixtures from asOfGW onward, blending
// current-season observations (before asOfGW's deadline) into the pre-season
// prior.
func (e *Engine) RunInSeason(asOfGW int) (*ProjectionOutput, error) {
	if asOfGW < 1 {
		asOfGW = 1
	}
	e.AsOfGameweek = asOfGW

	players, err := e.loadPrior()
	if err != nil {
		return nil, err
	}

	deadlines, err := LoadGameweekDeadlines(e.DB, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading gameweek deadlines: %w", err)
	}
	deadline, ok := deadlines[asOfGW]
	if !ok {
		return nil, fmt.Errorf("no deadline for gameweek %d in season %d", asOfGW, e.TargetSeason)
	}
	beforeDate := deadline.Format("2006-01-02")

	playerIDs := make([]int, len(players))
	for i := range players {
		playerIDs[i] = players[i].ID
	}

	curFixtures, err := LoadPlayerFixturesForSeasonUpTo(e.DB, playerIDs, e.TargetSeason, beforeDate)
	if err != nil {
		return nil, err
	}
	curGWs, err := LoadFPLGameweeksForSeasonUpTo(e.DB, playerIDs, e.TargetSeason, asOfGW)
	if err != nil {
		return nil, err
	}
	curTeam, err := LoadCurrentTeamStrengths(e.DB, e.TargetSeason, beforeDate)
	if err != nil {
		return nil, err
	}
	e.TeamStrengths = blendTeamStrengths(e.TeamStrengths, curTeam)
	e.computeLeagueAvgOff()

	observed := buildObservedStats(players, curFixtures, curGWs)
	e.availability, _ = LoadPlayerAvailability(e.DB, e.TargetSeason, playerIDs)

	e.fixturesByTeam, err = LoadFixturesWindow(e.DB, e.TargetSeason, asOfGW, 38)
	if err != nil {
		return nil, fmt.Errorf("loading remaining fixtures: %w", err)
	}

	projections := make([]PlayerProjection, 0, len(players))
	for i := range players {
		p := &players[i]
		in := blendInputs(e.priorInputs(p), observed[p.ID], p.Position)
		projections = append(projections, e.projectPlayer(p, in))
	}

	return e.finalize(projections), nil
}

// loadPrior loads the pre-season state shared by Run and RunInSeason:
// team strengths, position metadata, the player pool and prior-season
// rates.
func (e *Engine) loadPrior() ([]Player, error) {
	var err error
	e.TeamStrengths, err = LoadTeamStrengths(e.DB, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading team strengths: %w", err)
	}
	e.computeLeagueAvgOff()

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
		return nil, fmt.Errorf("loading CS rates: %w", err)
	}

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

	playerIDs := make([]int, len(players))
	for i, p := range players {
		playerIDs[i] = p.ID
	}

	fixtures, err := LoadPlayerFixtures(e.DB, playerIDs, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading fixtures: %w", err)
	}
	fplGWs, err := LoadFPLGameweeks(e.DB, playerIDs, e.TargetSeason)
	if err != nil {
		return nil, fmt.Errorf("loading FPL gameweeks: %w", err)
	}
	playerTeams, err := LoadPlayerTeams(e.DB, playerIDs, e.TargetSeason, e.Backtest)
	if err != nil {
		return nil, fmt.Errorf("loading player teams: %w", err)
	}

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

	e.computePositionalMeans(players, fixtures)
	return players, nil
}

// computeLeagueAvgOff recomputes the league-average offensive rating used
// for the team-context adjustment.
func (e *Engine) computeLeagueAvgOff() {
	var totalOff float64
	var teamCount int
	for _, ts := range e.TeamStrengths {
		totalOff += ts.OffensiveRating
		teamCount++
	}
	if teamCount > 0 {
		e.leagueAvgOffRating = totalOff / float64(teamCount)
	} else {
		e.leagueAvgOffRating = 0
	}
}

// finalize sorts, computes VORP and H2H adjustment, and wraps the output.
func (e *Engine) finalize(projections []PlayerProjection) *ProjectionOutput {
	sort.Slice(projections, func(i, j int) bool {
		return projections[i].ProjectedPoints > projections[j].ProjectedPoints
	})
	e.computeVORP(projections)
	e.computeH2HAdjusted(projections)
	sort.Slice(projections, func(i, j int) bool {
		return projections[i].VORP > projections[j].VORP
	})

	return &ProjectionOutput{
		Season:       e.TargetSeason,
		LeagueSize:   e.LeagueSize,
		AsOfGameweek: e.AsOfGameweek,
		Players:      projections,
	}
}

// PriorRates is the set of per-90 / per-match quantities fed into the
// fixture projection. Prior and posterior (blended) inputs share this shape.
type PriorRates struct {
	XGPer90           float64 `json:"xg_per_90"`
	XAPer90           float64 `json:"xa_per_90"`
	YellowsPer90      float64 `json:"yellows_per_90"`
	RedsPer90         float64 `json:"reds_per_90"`
	BonusPerMatch     float64 `json:"bonus_per_match"`
	MinutesPerFixture float64 `json:"minutes_per_fixture"`
}

// projRates is the fully-resolved per-player rate set used to project a
// single fixture.
type projRates struct {
	XGPer90             float64
	XAPer90             float64
	YellowsPer90        float64
	RedsPer90           float64
	FPLPer90            float64 // regression per-90 points
	BonusPerMatch       float64
	CSProb              float64
	DefconProb          float64
	GCMatch             float64
	SavesPerMatch       float64
	AvgAppearancePoints float64
	MinutesPerFixture   float64
}

// fixtureCounts holds the raw (non-points) per-fixture projections used to
// accumulate season-total component counts.
type fixtureCounts struct {
	Goals, Assists, CleanSheets, Bonus, Yellows, Reds, Defcon float64
}

// observedStats aggregates a player's current-season PL data up to the
// as-of cutoff.
type observedStats struct {
	Minutes  int
	Fixtures int // rows in players_fixtures, including zero-minute bench rows
	XG       float64
	XA       float64
	Yellows  int
	Reds     int
	BPS      int
	HasXG    bool
}

// priorInputs derives the pre-season rate set for a player: regressed,
// team-adjusted per-90 rates plus projected minutes.
func (e *Engine) priorInputs(p *Player) PriorRates {
	xgPer90 := e.regressRate(p.XGPer90, e.posMeanXGPer90[p.Position], p.WeightedRateMinutes)
	xaPer90 := e.regressRate(p.XAPer90, e.posMeanXAPer90[p.Position], p.WeightedRateMinutes)
	teamAdj := e.teamContextMultiplier(p.TeamID)

	in := PriorRates{
		XGPer90:           xgPer90 * teamAdj,
		XAPer90:           xaPer90 * teamAdj,
		YellowsPer90:      p.YellowsPer90,
		RedsPer90:         p.RedsPer90,
		MinutesPerFixture: e.projectMinutes(p) / 38.0,
	}

	bpsPer90 := p.BPSPer90
	if !p.HasFPLHistory || bpsPer90 == 0 {
		bpsPer90 = e.posMeanBPSPer90[p.Position]
	}
	in.BonusPerMatch = bonusFromBPS(p.Position, bpsPer90)
	return in
}

// buildObservedStats computes current-season PL statistics for each player.
func buildObservedStats(players []Player, curFixtures map[int][]playerFixtureRow, curGWs map[int][]fplGWRow) map[int]observedStats {
	result := make(map[int]observedStats, len(players))
	for _, p := range players {
		var s observedStats
		for _, f := range curFixtures[p.ID] {
			if !f.IsPL {
				continue
			}
			s.Fixtures++
			s.Minutes += f.Minutes
			if f.YellowCard {
				s.Yellows++
			}
			if f.RedCard {
				s.Reds++
			}
			if f.XG.Valid {
				s.XG += f.XG.Float64
				s.XA += f.XA.Float64
				s.HasXG = true
			}
		}
		for _, gw := range curGWs[p.ID] {
			s.BPS += gw.BPS
		}
		result[p.ID] = s
	}
	return result
}

// blendInputs Bayesian-updates the pre-season prior with current-season
// observations, shrinking observed rates by current-season sample size.
func blendInputs(prior PriorRates, obs observedStats, pos FPLPosition) PriorRates {
	out := prior
	if obs.Minutes <= 0 {
		return out
	}

	m := float64(obs.Minutes)
	k := rateShrinkageMinutes

	if obs.HasXG {
		per90 := m / 90.0
		out.XGPer90 = blendRate(prior.XGPer90, obs.XG/per90, m, k)
		out.XAPer90 = blendRate(prior.XAPer90, obs.XA/per90, m, k)
	}
	per90 := m / 90.0
	out.YellowsPer90 = blendRate(prior.YellowsPer90, float64(obs.Yellows)/per90, m, k)
	out.RedsPer90 = blendRate(prior.RedsPer90, float64(obs.Reds)/per90, m, k)

	if obs.BPS > 0 {
		obsBonus := bonusFromBPS(pos, float64(obs.BPS)/per90)
		out.BonusPerMatch = blendRate(prior.BonusPerMatch, obsBonus, m, k)
	}

	if obs.Fixtures > 0 {
		out.MinutesPerFixture = blendRate(
			prior.MinutesPerFixture,
			float64(obs.Minutes)/float64(obs.Fixtures),
			float64(obs.Fixtures),
			minutesShrinkageAppearances,
		)
	}

	return out
}

// blendRate performs the Bayesian shrinkage blend used throughout.
func blendRate(prior, observed, samples, k float64) float64 {
	if k <= 0 || samples <= 0 {
		return prior
	}
	return (prior*k + observed*samples) / (k + samples)
}

// bonusFromBPS converts BPS per 90 into expected bonus points per match.
// Calibrated from 2025 data: BPS 10/app -> 0.17 bonus/match, BPS 15 -> 0.37,
// BPS 20 -> 0.70. Capped since bonus goes to the top-3 BPS in each match.
func bonusFromBPS(pos FPLPosition, bpsPer90 float64) float64 {
	bonus := math.Max(0, 0.053*bpsPer90-0.36)
	bonus = math.Min(bonus, 0.75)
	// GKs accumulate high BPS from saves but rarely win the bonus.
	if pos == Goalkeeper {
		bonus = math.Min(bonus, 0.35)
	}
	return bonus
}

// buildProjRates resolves rate inputs into everything needed to project a
// single fixture.
func (e *Engine) buildProjRates(p *Player, in PriorRates) projRates {
	csProb := e.teamCSProbability(p.TeamID)

	var gcPerMatch float64
	if ts, ok := e.TeamStrengths[p.TeamID]; ok {
		gcPerMatch = ts.GoalsConcededPerMatch
	} else {
		gcPerMatch = 1.3
	}

	var savesPerMatch float64
	if p.Position == Goalkeeper {
		if ts, ok := e.TeamStrengths[p.TeamID]; ok {
			savesPerMatch = 1.0 + 1.0*ts.GoalsConcededPerMatch
			if savesPerMatch < 1.5 {
				savesPerMatch = 1.5
			}
		} else {
			savesPerMatch = 2.3
		}
	}

	// Regression model: FPL_per90 from per-90 stats + team context.
	// Coefficients learned from 2020-2024 PL data via weighted OLS.
	var fplPer90 float64
	switch p.Position {
	case Goalkeeper:
		fplPer90 = -1.2808 + 9.6820*in.XGPer90 + 4.0947*in.XAPer90 + 8.4996*in.YellowsPer90 +
			11.2793*csProb + 1.4993*gcPerMatch
	case Defender:
		fplPer90 = 0.6733 + 6.0066*in.XGPer90 + 4.8363*in.XAPer90 + 1.2720*in.YellowsPer90 +
			7.5786*csProb + 0.0011*gcPerMatch
	case Midfielder:
		fplPer90 = 0.8004 + 6.6982*in.XGPer90 + 3.5295*in.XAPer90 - 0.0204*in.YellowsPer90 +
			3.6206*csProb + 0.3866*gcPerMatch
	case Forward:
		fplPer90 = 3.0536 + 3.7699*in.XGPer90 + 5.1795*in.XAPer90 + 1.6508*in.YellowsPer90 -
			0.6691*csProb - 0.2469*gcPerMatch
	}

	// Appearance-point split: fraction of appearances that are 60+ minutes.
	// seasonMinutes is the full-season equivalent at this per-match rate.
	seasonMinutes := in.MinutesPerFixture * 38.0
	matchesPlayed := math.Min(seasonMinutes/60.0, 38.0)
	var avgMinPerMatch float64
	if matchesPlayed > 0 {
		avgMinPerMatch = seasonMinutes / matchesPlayed
	}

	var fullMatchFrac float64
	switch p.Position {
	case Defender:
		fullMatchFrac = 0.80
		if avgMinPerMatch > 0 {
			fullMatchFrac = math.Min(0.98, fullMatchFrac*(avgMinPerMatch/77.0))
		}
	case Midfielder:
		fullMatchFrac = 0.64
		if avgMinPerMatch > 0 {
			fullMatchFrac = math.Min(0.98, fullMatchFrac*(avgMinPerMatch/67.0))
		}
	default:
		fullMatchFrac = 0.75
		if avgMinPerMatch >= 75 {
			fullMatchFrac = 0.9
		} else if avgMinPerMatch >= 60 {
			fullMatchFrac = 0.8
		} else if avgMinPerMatch > 0 {
			fullMatchFrac = 0.4
		}
	}

	return projRates{
		XGPer90:             in.XGPer90,
		XAPer90:             in.XAPer90,
		YellowsPer90:        in.YellowsPer90,
		RedsPer90:           in.RedsPer90,
		FPLPer90:            fplPer90,
		BonusPerMatch:       in.BonusPerMatch,
		CSProb:              csProb,
		DefconProb:          e.defconProbability(p),
		GCMatch:             gcPerMatch,
		SavesPerMatch:       savesPerMatch,
		AvgAppearancePoints: fullMatchFrac*2.0 + (1.0-fullMatchFrac)*1.0,
		MinutesPerFixture:   in.MinutesPerFixture,
	}
}

// projectPlayer generates a full projection for a single player over their
// remaining fixtures and sums the totals.
func (e *Engine) projectPlayer(p *Player, in PriorRates) PlayerProjection {
	rates := e.buildProjRates(p, in)

	proj := PlayerProjection{
		PlayerID:  p.ID,
		FirstName: p.FirstName,
		LastName:  p.LastName,
		TeamID:    p.TeamID,
		TeamName:  p.TeamName,
		Position:  p.Position.String(),
	}

	// Note: it is tempting to return an empty projection here for anyone
	// players_teams does not place at a Premier League club, since they
	// have no Premier League fixtures to score in. Don't: that signal is
	// too weak to act on. Backtesting 2025 zeroed 196 players, 62 of whom
	// went on to score (one of them 121 points), because the mapping
	// lags late-window signings and has gaps. The softer minutes discount
	// in projectMinutes is the right hedge; positive evidence of a
	// departure comes from the availability feed instead, via status "u".
	fixtures := e.fixturesByTeam[p.TeamID]
	if len(fixtures) == 0 {
		// No schedule (e.g. missing fixtures_fpl_gameweeks rows): fall back
		// to a flat 38-match season with neutral difficulty.
		fixtures = syntheticSeason()
	}

	var av *Availability
	if a, ok := e.availability[p.ID]; ok {
		av = &a
	}
	byGW := make(map[int][]TeamFixture, len(fixtures))
	for _, fx := range fixtures {
		byGW[fx.Gameweek] = append(byGW[fx.Gameweek], fx)
	}

	for i, fx := range fixtures {
		m := FixtureMultipliers(byGW[fx.Gameweek], e.TeamStrengths)
		fp, fc := e.projectFixture(p, rates, fx, m, av, i)

		proj.Gameweeks = append(proj.Gameweeks, fp)
		proj.ProjectedMinutes += fp.ExpectedMinutes
		proj.ProjectedGoals += fc.Goals
		proj.ProjectedAssists += fc.Assists
		proj.ProjectedCleanSheets += fc.CleanSheets
		proj.ProjectedBonus += fc.Bonus
		proj.ProjectedYellows += fc.Yellows
		proj.ProjectedReds += fc.Reds
		proj.ProjectedDEFCON += fc.Defcon

		proj.AppearancePoints += fp.AppearancePoints
		proj.GoalPoints += fp.GoalPoints
		proj.AssistPoints += fp.AssistPoints
		proj.CleanSheetPoints += fp.CleanSheetPoints
		proj.SavePoints += fp.SavePoints
		proj.BonusPoints += fp.BonusPoints
		proj.CardPoints += fp.CardPoints
		proj.GoalsConcededPen += fp.GoalsConcededPen
		proj.DEFCONPoints += fp.DEFCONPoints
		proj.ProjectedPoints += fp.ProjectedPoints
	}

	// Consistency metrics.
	if len(p.HistoricGWPoints) > 0 {
		_, stddev := meanStdDev(p.HistoricGWPoints)
		proj.Consistency = stddev
		proj.Floor = percentile(p.HistoricGWPoints, 0.10)
	} else {
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

// projectFixture projects a single fixture, applying opponent difficulty and
// availability scaling to the expected minutes. fixtureIdx is the fixture's
// position in the projected schedule, used to age out injuries and
// suspensions with no known return date.
func (e *Engine) projectFixture(p *Player, r projRates, fx TeamFixture, m FixtureDifficulty, av *Availability, fixtureIdx int) (FixtureProjection, fixtureCounts) {
	fp := FixtureProjection{
		Gameweek:     fx.Gameweek,
		FixtureID:    fx.FixtureID,
		OpponentID:   fx.OpponentID,
		OpponentName: fx.OpponentName,
		IsHome:       fx.IsHome,
	}

	minutes := r.MinutesPerFixture
	if av != nil {
		minutes *= e.availabilityScale(av, fx.Date, fixtureIdx)
	}
	if minutes <= 0 {
		return fp, fixtureCounts{}
	}

	fp.ExpectedMinutes = minutes
	matchShare := minutes / 90.0
	appearance := math.Min(1.0, minutes/60.0)

	fp.AppearancePoints = appearance * r.AvgAppearancePoints

	goals := r.XGPer90 * matchShare * m.Attack
	assists := r.XAPer90 * matchShare * m.Attack
	cleanSheets := r.CSProb * appearance * m.Defense
	yellows := r.YellowsPer90 * matchShare
	reds := r.RedsPer90 * matchShare
	bonus := r.BonusPerMatch * appearance
	defcon := r.DefconProb * appearance

	fp.GoalPoints = goals * e.Rules.GoalPoints(p.Position)
	fp.AssistPoints = assists * e.Rules.AssistPoints
	fp.CleanSheetPoints = cleanSheets * e.Rules.CleanSheetPoints(p.Position)
	if p.Position == Goalkeeper {
		fp.SavePoints = r.SavesPerMatch * appearance / e.Rules.SavesDivisor
	}
	fp.BonusPoints = bonus
	fp.CardPoints = yellows*e.Rules.YellowPoints + reds*e.Rules.RedPoints
	if p.Position == Goalkeeper || p.Position == Defender {
		fp.GoalsConcededPen = -(r.GCMatch * appearance * m.Defense / e.Rules.GoalsConcededDivisor)
	}
	fp.DEFCONPoints = defcon * e.Rules.DefconPoints

	manual := fp.AppearancePoints +
		fp.GoalPoints +
		fp.AssistPoints +
		fp.CleanSheetPoints +
		fp.SavePoints +
		fp.BonusPoints +
		fp.CardPoints +
		fp.GoalsConcededPen +
		fp.DEFCONPoints

	regression := r.FPLPer90 * matchShare * m.Blended(p.Position)
	fp.ProjectedPoints = 0.60*manual + 0.40*regression

	return fp, fixtureCounts{
		Goals:       goals,
		Assists:     assists,
		CleanSheets: cleanSheets,
		Bonus:       bonus,
		Yellows:     yellows,
		Reds:        reds,
		Defcon:      defcon,
	}
}

// availabilityScale returns the minutes scale for a fixture given a player's
// availability. Where a return date is known it is honoured exactly: zero
// before it, full from it. Where one is not, an injury or suspension is aged
// out over the following fixtures rather than being treated as
// season-ending, while a player who has left the club is not.
//
// fixtureIdx is the fixture's position in the projected schedule, so index 0
// is the next match to be played.
func (e *Engine) availabilityScale(av *Availability, fixtureDate string, fixtureIdx int) float64 {
	if av == nil {
		return 1.0
	}
	isFirst := fixtureIdx == 0
	status := strings.TrimSpace(av.Status)
	switch status {
	case "":
		return 1.0
	case "u", "n":
		// Unavailable or not in the squad: sold, loaned out or
		// unregistered. Gone for the season unless a return is known.
		if av.NewsReturn != "" && fixtureDate != "" && fixtureDate >= av.NewsReturn {
			return 1.0
		}
		return 0.0
	case "i", "s":
		if av.NewsReturn != "" && fixtureDate != "" {
			if fixtureDate >= av.NewsReturn {
				return 1.0
			}
			return 0.0
		}
		// No return date given ("Unknown return date"), so treat the
		// absence as a hazard: certainly out for the next match, then
		// increasingly likely to be back.
		tau := injuryReturnTau
		if status == "s" {
			tau = suspensionReturnTau
		}
		return 1 - math.Exp(-float64(fixtureIdx)/tau)
	case "d":
		if isFirst {
			if av.ChanceOfPlayingNext > 0 {
				return float64(av.ChanceOfPlayingNext) / 100.0
			}
			return 0.5
		}
		return 1.0
	default: // "a" (available)
		if isFirst && av.ChanceOfPlayingNext > 0 && av.ChanceOfPlayingNext < 100 {
			return float64(av.ChanceOfPlayingNext) / 100.0
		}
		return 1.0
	}
}

// syntheticSeason returns a flat 38-gameweek schedule with no opponent
// information, used when a team has no fixture mapping.
func syntheticSeason() []TeamFixture {
	fxs := make([]TeamFixture, 38)
	for i := range fxs {
		fxs[i] = TeamFixture{Gameweek: i + 1, IsHome: true}
	}
	return fxs
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
		minutes int
		goals   int
		assists int
		yellows int
		reds    int
		xg      float64
		xa      float64
		npxg    float64
		xgCount int
		// PL only.
		plMinutes int
		plGoals   int
		plAssists int
		plYellows int
		plReds    int
		plXG      float64
		plXA      float64
		plNPXG    float64
		plXGCount int
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

// blendTeamStrengths blends current-season team form into the prior
// strengths using match-count shrinkage. Teams with no prior rating are
// taken from current-season data directly.
func blendTeamStrengths(prior, current map[int]*TeamStrength) map[int]*TeamStrength {
	out := make(map[int]*TeamStrength, len(prior)+len(current))
	for id, ts := range prior {
		cp := *ts
		out[id] = &cp
	}
	for id, cur := range current {
		ps, ok := out[id]
		if !ok {
			cp := *cur
			out[id] = &cp
			continue
		}
		m := float64(cur.Matches)
		if m <= 0 {
			continue
		}
		w := m / (m + teamShrinkageMatches)
		ps.OffensiveRating = ps.OffensiveRating*(1-w) + cur.OffensiveRating*w
		ps.DefensiveRating = ps.DefensiveRating*(1-w) + cur.DefensiveRating*w
		ps.GoalsConcededPerMatch = ps.GoalsConcededPerMatch*(1-w) + cur.GoalsConcededPerMatch*w
		// Recompute the Poisson clean-sheet probability from the blended
		// defensive rating; the 70/30 actual-vs-Poisson blend in
		// teamCSProbability keeps using this as its Poisson component.
		ps.CleanSheetProb = math.Exp(-ps.DefensiveRating)
	}
	return out
}

// computeVORP calculates Value Over Replacement Player for each projection.
func (e *Engine) computeVORP(projections []PlayerProjection) {
	// Count how many of each position are drafted in an N-team league.
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
