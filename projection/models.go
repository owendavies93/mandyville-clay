package projection

// FPLPosition represents the four FPL position categories.
type FPLPosition int

const (
	Goalkeeper FPLPosition = 1
	Defender   FPLPosition = 2
	Midfielder FPLPosition = 3
	Forward    FPLPosition = 4
)

func (p FPLPosition) String() string {
	switch p {
	case Goalkeeper:
		return "GK"
	case Defender:
		return "DEF"
	case Midfielder:
		return "MID"
	case Forward:
		return "FWD"
	default:
		return "???"
	}
}

// GoalPoints returns FPL points per goal for this position.
func (p FPLPosition) GoalPoints() float64 {
	switch p {
	case Goalkeeper, Defender:
		return 6
	case Midfielder:
		return 5
	case Forward:
		return 4
	default:
		return 0
	}
}

// CleanSheetPoints returns FPL points per clean sheet for this position.
func (p FPLPosition) CleanSheetPoints() float64 {
	switch p {
	case Goalkeeper, Defender:
		return 4
	case Midfielder:
		return 1
	default:
		return 0
	}
}

// ScoringRules holds the points values for a scoring system. The classic
// and draft games differ in a few places (notably GK goals: 6 vs 10, and
// draft having no captains), so the engine parameterises on this rather
// than baking one set of constants in.
type ScoringRules struct {
	Name                                                      string
	GoalPointsGK, GoalPointsDEF, GoalPointsMID, GoalPointsFWD float64
	AssistPoints                                              float64
	CleanSheetGK, CleanSheetDEF, CleanSheetMID, CleanSheetFWD float64
	YellowPoints, RedPoints                                   float64
	GoalsConcededDivisor                                      float64 // lose 1 pt per N goals conceded (GK/DEF)
	SavesDivisor                                              float64 // 1 pt per N saves (GK)
	DefconPoints                                              float64 // points per DEFCON hit
}

// ClassicRules is the standard FPL scoring used by the classic game.
var ClassicRules = ScoringRules{
	Name:                 "classic",
	GoalPointsGK:         6,
	GoalPointsDEF:        6,
	GoalPointsMID:        5,
	GoalPointsFWD:        4,
	AssistPoints:         3,
	CleanSheetGK:         4,
	CleanSheetDEF:        4,
	CleanSheetMID:        1,
	CleanSheetFWD:        0,
	YellowPoints:         -1,
	RedPoints:            -3,
	GoalsConcededDivisor: 2,
	SavesDivisor:         3,
	DefconPoints:         2,
}

// DraftRules is the FPL Draft scoring system, sourced from
// draft.premierleague.com/api/bootstrap-static settings.scoring.
// Differences from classic: GK goals worth 10, captains disabled.
var DraftRules = ScoringRules{
	Name:                 "draft",
	GoalPointsGK:         10,
	GoalPointsDEF:        6,
	GoalPointsMID:        5,
	GoalPointsFWD:        4,
	AssistPoints:         3,
	CleanSheetGK:         4,
	CleanSheetDEF:        4,
	CleanSheetMID:        1,
	CleanSheetFWD:        0,
	YellowPoints:         -1,
	RedPoints:            -3,
	GoalsConcededDivisor: 2,
	SavesDivisor:         3,
	DefconPoints:         2,
}

// GoalPoints returns points per goal for a position under these rules.
func (r ScoringRules) GoalPoints(pos FPLPosition) float64 {
	switch pos {
	case Goalkeeper:
		return r.GoalPointsGK
	case Defender:
		return r.GoalPointsDEF
	case Midfielder:
		return r.GoalPointsMID
	case Forward:
		return r.GoalPointsFWD
	default:
		return 0
	}
}

// CleanSheetPoints returns points per clean sheet for a position.
func (r ScoringRules) CleanSheetPoints(pos FPLPosition) float64 {
	switch pos {
	case Goalkeeper:
		return r.CleanSheetGK
	case Defender:
		return r.CleanSheetDEF
	case Midfielder:
		return r.CleanSheetMID
	case Forward:
		return r.CleanSheetFWD
	default:
		return 0
	}
}

// Player holds all data needed for projection.
type Player struct {
	ID        int
	FirstName string
	LastName  string
	TeamID    int
	TeamName  string
	Position  FPLPosition

	// Historical per-90 rates (from fixture data).
	MinutesPerSeason []SeasonMinutes
	XGPer90          float64
	XAPer90          float64
	GoalsPer90       float64
	AssistsPer90     float64
	YellowsPer90     float64
	RedsPer90        float64
	NPXGPer90        float64

	// From FPL gameweek history.
	BPSPer90         float64
	HistoricGWPoints []float64 // individual gameweek point totals
	HasFPLHistory    bool

	// Granular position from match data (for DEFCON estimation).
	PrimaryDetailedPos string

	// Whether the player's current team is in the PL.
	IsOnPLTeam bool

	// Age at start of season (0 if unknown).
	Age int

	// Weighted minutes used for rate calculations (from used seasons only).
	WeightedRateMinutes float64

	// Whether this player had no PL minutes in recent seasons
	// (returning transfer or first PL stint).
	IsTransferIn bool

	// Raw (undiscounted) minutes across recent seasons.
	RawMinutesRecent float64
}

// SeasonMinutes tracks total minutes played in a season with a recency weight.
type SeasonMinutes struct {
	Season  int
	Minutes int
	Weight  float64
}

// TeamStrength holds attacking and defensive strength ratings for a team.
type TeamStrength struct {
	TeamID   int
	TeamName string

	// Offensive: average xG per match (weighted recent seasons).
	OffensiveRating float64
	// Defensive: average xG conceded per match.
	DefensiveRating float64
	// Clean sheet probability per match.
	CleanSheetProb float64
	// Average goals conceded per match.
	GoalsConcededPerMatch float64

	// Matches is the number of matches behind this rating, used to weight
	// in-season blending against the prior.
	Matches int
}

// PlayerProjection is the final output for a single player.
type PlayerProjection struct {
	PlayerID  int    `json:"player_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	TeamID    int    `json:"team_id"`
	TeamName  string `json:"team"`
	Position  string `json:"position"`

	// Component projections.
	ProjectedMinutes     float64 `json:"projected_minutes"`
	ProjectedGoals       float64 `json:"projected_goals"`
	ProjectedAssists     float64 `json:"projected_assists"`
	ProjectedCleanSheets float64 `json:"projected_clean_sheets"`
	ProjectedBonus       float64 `json:"projected_bonus"`
	ProjectedYellows     float64 `json:"projected_yellows"`
	ProjectedReds        float64 `json:"projected_reds"`
	ProjectedDEFCON      float64 `json:"projected_defcon"`

	// Points breakdown.
	AppearancePoints float64 `json:"appearance_points"`
	GoalPoints       float64 `json:"goal_points"`
	AssistPoints     float64 `json:"assist_points"`
	CleanSheetPoints float64 `json:"clean_sheet_points"`
	SavePoints       float64 `json:"save_points"`
	BonusPoints      float64 `json:"bonus_points"`
	CardPoints       float64 `json:"card_points"`
	GoalsConcededPen float64 `json:"goals_conceded_penalty"`
	DEFCONPoints     float64 `json:"defcon_points"`

	// Totals.
	ProjectedPoints float64 `json:"projected_points"`

	// H2H metrics.
	Consistency    float64 `json:"consistency"` // std dev of GW points
	Floor          float64 `json:"floor"`       // 10th percentile GW points
	H2HAdjustedPts float64 `json:"h2h_adjusted_points"`

	// Draft value.
	VORP float64 `json:"vorp"`

	// Per-fixture projections; the totals above are the sum of these.
	// Fixtures with expected minutes of zero (e.g. blanks, injury) are
	// included so consumers can see the full schedule.
	Gameweeks []FixtureProjection `json:"gameweeks"`
}

// ProjectionOutput is the top-level structure written to JSON.
type ProjectionOutput struct {
	Season       int                `json:"season"`
	LeagueSize   int                `json:"league_size"`
	AsOfGameweek int                `json:"as_of_gameweek,omitempty"` // 0 = pre-season
	Players      []PlayerProjection `json:"players"`
}

// TeamFixture is a single fixture for a team in a given gameweek.
type TeamFixture struct {
	Gameweek     int
	FixtureID    int
	OpponentID   int
	OpponentName string
	IsHome       bool
	Date         string // YYYY-MM-DD (zero value if unknown)
}

// FixtureProjection is the projected points breakdown for a single fixture.
type FixtureProjection struct {
	Gameweek        int     `json:"gameweek"`
	FixtureID       int     `json:"fixture_id"`
	OpponentID      int     `json:"opponent_team_id"`
	OpponentName    string  `json:"opponent"`
	IsHome          bool    `json:"is_home"`
	ExpectedMinutes float64 `json:"expected_minutes"`

	AppearancePoints float64 `json:"appearance_points"`
	GoalPoints       float64 `json:"goal_points"`
	AssistPoints     float64 `json:"assist_points"`
	CleanSheetPoints float64 `json:"clean_sheet_points"`
	SavePoints       float64 `json:"save_points"`
	BonusPoints      float64 `json:"bonus_points"`
	CardPoints       float64 `json:"card_points"`
	GoalsConcededPen float64 `json:"goals_conceded_penalty"`
	DEFCONPoints     float64 `json:"defcon_points"`

	ProjectedPoints float64 `json:"projected_points"`
}

// Availability is a player's injury/suspension status at a point in time,
// as synced from the FPL API into fpl_player_availability.
type Availability struct {
	PlayerID            int
	Status              string // "a", "d", "i", "s", "u", "n" or ""
	ChanceOfPlayingNext int    // 0-100, 0 if unknown
	NewsReturn          string // YYYY-MM-DD, "" if unknown
}

// PlayerPrice holds a player's starting price for a season.
// Team assignment comes from LoadPlayerTeams (players_teams table).
type PlayerPrice struct {
	Price float64
}
