package projection

import "math"

// FixtureDifficultyMultiplier computes a scaling factor for a player's
// projected points over a set of fixtures, based on the strength of the
// opponents faced relative to the league average.
//
// The full-season projection already incorporates the player's own team
// strength; this adjusts for the specific opponents in the fixture window.
// A value > 1 means an easier-than-average run, < 1 means harder.
//
//   - attacking output: compares opponents' goals conceded per match to
//     the league average (leakier opponents -> higher multiplier)
//   - defensive output: compares opponents' goals scored per match to the
//     league average (weaker attacks -> higher multiplier)
func FixtureDifficultyMultiplier(fixtures []TeamFixture, strengths map[int]*TeamStrength, pos FPLPosition) float64 {
	if len(fixtures) == 0 {
		return 1.0
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
		return 1.0
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

	attackMult := math.Sqrt(attackRatio / float64(len(fixtures)))
	defenseMult := math.Sqrt(defenseRatio / float64(len(fixtures)))

	// Blend by position: GKs and DEFs benefit mostly from clean sheets,
	// FWDs purely from attacking returns, MIDs in between.
	switch pos {
	case Goalkeeper:
		return defenseMult
	case Defender:
		return 0.75*defenseMult + 0.25*attackMult
	case Midfielder:
		return 0.90*attackMult + 0.10*defenseMult
	case Forward:
		return attackMult
	default:
		return (attackMult + defenseMult) / 2
	}
}
