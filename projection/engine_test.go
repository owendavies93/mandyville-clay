package projection

import (
	"math"
	"testing"
)

func TestScoringRulesGoalPoints(t *testing.T) {
	cases := []struct {
		rules ScoringRules
		pos   FPLPosition
		want  float64
	}{
		{ClassicRules, Goalkeeper, 6},
		{ClassicRules, Defender, 6},
		{ClassicRules, Midfielder, 5},
		{ClassicRules, Forward, 4},
		{DraftRules, Goalkeeper, 10},
		{DraftRules, Defender, 6},
		{DraftRules, Midfielder, 5},
		{DraftRules, Forward, 4},
	}
	for _, c := range cases {
		if got := c.rules.GoalPoints(c.pos); got != c.want {
			t.Errorf("%s GoalPoints(%s) = %v, want %v", c.rules.Name, c.pos, got, c.want)
		}
	}
}

func TestScoringRulesCleanSheetPoints(t *testing.T) {
	if got := ClassicRules.CleanSheetPoints(Goalkeeper); got != 4 {
		t.Errorf("GK clean sheet = %v, want 4", got)
	}
	if got := DraftRules.CleanSheetPoints(Forward); got != 0 {
		t.Errorf("FWD clean sheet = %v, want 0", got)
	}
}

func TestBlendRate(t *testing.T) {
	// No observed sample: stick with the prior.
	if got := blendRate(10, 0, 0, 900); got != 10 {
		t.Errorf("no samples = %v, want 10", got)
	}
	// Equal sample size and shrinkage constant: exact midpoint.
	mid := blendRate(10, 20, 900, 900)
	if math.Abs(mid-15) > 1e-9 {
		t.Errorf("midpoint = %v, want 15", mid)
	}
	// Infinite samples: converge to observed.
	conv := blendRate(10, 20, 1e9, 900)
	if math.Abs(conv-20) > 1e-4 {
		t.Errorf("convergence = %v, want 20", conv)
	}
}

func TestFixtureMultipliers(t *testing.T) {
	// Empty fixtures → neutral.
	m := FixtureMultipliers(nil, nil)
	if m.Attack != 1 || m.Defense != 1 {
		t.Errorf("empty = %+v, want neutral", m)
	}

	// Team A: average (off 1.5, gc 1.5). Team B: weak (off 0.5, gc 2.5).
	strengths := map[int]*TeamStrength{
		1: {OffensiveRating: 1.5, GoalsConcededPerMatch: 1.5},
		2: {OffensiveRating: 0.5, GoalsConcededPerMatch: 2.5},
	}
	// Facing team B: leaky defence (attack easier) and weak attack (defence easier).
	fx := []TeamFixture{{OpponentID: 2}}
	m = FixtureMultipliers(fx, strengths)
	if m.Attack <= 1 || m.Defense <= 1 {
		t.Errorf("vs weak opponent = %+v, want both > 1", m)
	}
}

func TestAvailabilityScale(t *testing.T) {
	e := &Engine{}

	// No availability info: full minutes.
	if got := e.availabilityScale(nil, "", 0); got != 1 {
		t.Errorf("nil availability = %v, want 1", got)
	}

	// A known return date is honoured exactly, whichever fixture it is.
	injured := &Availability{Status: "i", NewsReturn: "2026-09-01"}
	if got := e.availabilityScale(injured, "2026-08-20", 0); got != 0 {
		t.Errorf("injured before return = %v, want 0", got)
	}
	if got := e.availabilityScale(injured, "2026-09-02", 4); got != 1 {
		t.Errorf("injured after return = %v, want 1", got)
	}

	// Doubtful, first fixture, 75% chance.
	if got := e.availabilityScale(&Availability{Status: "d", ChanceOfPlayingNext: 75}, "", 0); got != 0.75 {
		t.Errorf("doubtful 75%% = %v, want 0.75", got)
	}
	// Doubtful with no percentage: default 50%.
	if got := e.availabilityScale(&Availability{Status: "d"}, "", 0); got != 0.5 {
		t.Errorf("doubtful no chance = %v, want 0.5", got)
	}
	// Doubtful only dents the imminent fixture.
	if got := e.availabilityScale(&Availability{Status: "d"}, "", 1); got != 1 {
		t.Errorf("doubtful later fixture = %v, want 1", got)
	}

	// Available, late test at 50%.
	if got := e.availabilityScale(&Availability{Status: "a", ChanceOfPlayingNext: 50}, "", 0); got != 0.5 {
		t.Errorf("available 50%% = %v, want 0.5", got)
	}
	// Available, no flag: full.
	if got := e.availabilityScale(&Availability{Status: "a"}, "", 0); got != 1 {
		t.Errorf("available = %v, want 1", got)
	}
}

// An injury or suspension with no return date must not write off the whole
// season: the player is out for the next match, then increasingly likely to
// feature. Leaving the club, by contrast, is season-ending.
func TestAvailabilityScaleUnknownReturn(t *testing.T) {
	e := &Engine{}

	injured := &Availability{Status: "i"}
	if got := e.availabilityScale(injured, "2026-08-20", 0); got != 0 {
		t.Errorf("injured, next fixture = %v, want 0", got)
	}

	prev := 0.0
	for idx := 1; idx <= 10; idx++ {
		got := e.availabilityScale(injured, "", idx)
		if got <= prev {
			t.Errorf("injured at fixture %d = %v, want > %v", idx, got, prev)
		}
		if got >= 1 {
			t.Errorf("injured at fixture %d = %v, want < 1", idx, got)
		}
		prev = got
	}

	// 1-exp(-3/3) three fixtures out.
	if got := e.availabilityScale(injured, "", 3); math.Abs(got-0.6321) > 1e-3 {
		t.Errorf("injured at fixture 3 = %v, want ~0.632", got)
	}

	// Suspensions are shorter and more certain, so they recover faster.
	suspended := &Availability{Status: "s"}
	if got := e.availabilityScale(suspended, "", 0); got != 0 {
		t.Errorf("suspended, next fixture = %v, want 0", got)
	}
	if s, i := e.availabilityScale(suspended, "", 2), e.availabilityScale(injured, "", 2); s <= i {
		t.Errorf("suspended at fixture 2 = %v, want > injured %v", s, i)
	}

	// Sold, loaned out or unregistered: gone for the season.
	for _, status := range []string{"u", "n"} {
		for _, idx := range []int{0, 5, 20} {
			got := e.availabilityScale(&Availability{Status: status}, "", idx)
			if got != 0 {
				t.Errorf("status %q at fixture %d = %v, want 0", status, idx, got)
			}
		}
	}
}

func TestSyntheticSeason(t *testing.T) {
	fxs := syntheticSeason()
	if len(fxs) != 38 {
		t.Fatalf("synthetic season length = %d, want 38", len(fxs))
	}
	if fxs[0].Gameweek != 1 || fxs[37].Gameweek != 38 {
		t.Errorf("synthetic season gameweeks = %d..%d, want 1..38", fxs[0].Gameweek, fxs[37].Gameweek)
	}
}
