package main

// ProjectionData mirrors the JSON structure from the projection engine.
type ProjectionData struct {
	Season     int      `json:"season"`
	LeagueSize int      `json:"league_size"`
	Players    []Player `json:"players"`
}

type Player struct {
	PlayerID  int    `json:"player_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	TeamName  string `json:"team"`
	Position  string `json:"position"`

	ProjectedMinutes     float64 `json:"projected_minutes"`
	ProjectedGoals       float64 `json:"projected_goals"`
	ProjectedAssists     float64 `json:"projected_assists"`
	ProjectedCleanSheets float64 `json:"projected_clean_sheets"`
	ProjectedBonus       float64 `json:"projected_bonus"`

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
	Consistency     float64 `json:"consistency"`
	Floor           float64 `json:"floor"`
	H2HAdjustedPts  float64 `json:"h2h_adjusted_points"`
	VORP            float64 `json:"vorp"`
}

// DraftState tracks which players have been picked and by whom.
type DraftState int

const (
	Available DraftState = iota
	DraftedByMe
	TakenByOpponent
)
