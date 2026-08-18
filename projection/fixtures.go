package projection

import "math"

// FixtureDifficulty holds the attacking and defensive difficulty scaling
// factors for a set of fixtures. Values > 1 mean an easier-than-average
// run for that facet, < 1 means harder.
type FixtureDifficulty struct {
	Attack  float64
	Defense float64
}

// FixtureMultipliers computes the attacking and defensive difficulty of a
// set of fixtures relative to league averages. The full-season projection
// already incorporates the player's own team strength; this adjusts for the
// specific opponents in the window.
//
//   - attacking output: compares opponents' goals conceded per match to the
//     league average (leakier opponents -> higher multiplier)
//   - defensive output: compares opponents' goals scored per match to the
//     league average (weaker attacks -> higher multiplier)
func FixtureMultipliers(fixtures []TeamFixture, strengths map[int]*TeamStrength) FixtureDifficulty {
	if len(fixtures) == 0 {
		return FixtureDifficulty{Attack: 1.0, Defense: 1.0}
	}

	// League averages from the strengths map.
	var leagueAvgOff, leagueAvgGC float64
	var n int
	for _, ts := range strengths {
		leagueAvgOff += ts.OffensiveRating
		leagueAvgGC += ts.GoalsConcededPerMatch
		n++
	}
	if n == 0 || leagueAvgOff == 0 || leagueAvgGC == 0 {
		return FixtureDifficulty{Attack: 1.0, Defense: 1.0}
	}
	leagueAvgOff /= float64(n)
	leagueAvgGC /= float64(n)

	var attackRatio, defenseRatio float64
	for _, fx := range fixtures {
		var oppOff, oppGC float64
		if ts, ok := strengths[fx.OpponentID]; ok {
			oppOff = ts.OffensiveRating
			oppGC = ts.GoalsConcededPerMatch
		} else {
			// Unknown opponent (e.g. promoted side): treat as average.
			oppOff = leagueAvgOff
			oppGC = leagueAvgGC
		}
		attackRatio += oppGC / leagueAvgGC
		defenseRatio += leagueAvgOff / oppOff
	}

	return FixtureDifficulty{
		Attack:  math.Sqrt(attackRatio / float64(len(fixtures))),
		Defense: math.Sqrt(defenseRatio / float64(len(fixtures))),
	}
}

// Blended returns a single position-appropriate difficulty factor for the
// multipliers. GKs and DEFs benefit mostly from clean sheets, FWDs purely
// from attacking returns, MIDs in between.
func (m FixtureDifficulty) Blended(pos FPLPosition) float64 {
	switch pos {
	case Goalkeeper:
		return m.Defense
	case Defender:
		return 0.75*m.Defense + 0.25*m.Attack
	case Midfielder:
		return 0.90*m.Attack + 0.10*m.Defense
	case Forward:
		return m.Attack
	default:
		return (m.Attack + m.Defense) / 2
	}
}

// FixtureDifficultyMultiplier computes a single blended scaling factor for
// a player's projected points over a set of fixtures, based on the strength
// of the opponents faced relative to the league average.
func FixtureDifficultyMultiplier(fixtures []TeamFixture, strengths map[int]*TeamStrength, pos FPLPosition) float64 {
	return FixtureMultipliers(fixtures, strengths).Blended(pos)
}
