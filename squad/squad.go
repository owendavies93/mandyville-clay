// Package squad holds the game-agnostic FPL squad primitives shared by
// the draft and classic games: player projections, the fixed 2/5/5/3
// squad shape, the starting-XI optimiser and marginal-value helpers.
package squad

import (
	"fmt"
	"sort"
)

// Position constants, matching the engine's output.
const (
	PosGK  = "GK"
	PosDEF = "DEF"
	PosMID = "MID"
	PosFWD = "FWD"
)

// SquadShape is the fixed FPL squad composition (2 GK, 5 DEF, 5 MID,
// 3 FWD), shared by both the draft and classic games.
var SquadShape = map[string]int{PosGK: 2, PosDEF: 5, PosMID: 5, PosFWD: 3}

// Player is a projectable player: identity, position and projected points
// per gameweek, plus the engine's consistency metric (a stddev of historic
// gameweek points, used by the draft H2H view).
type Player struct {
	ID          int
	Name        string
	Position    string
	GWPoints    map[int]float64 // gameweek -> projected points
	Consistency float64         // stddev of historic gameweek points
}

// PointsIn returns the player's projected points for a gameweek (0 if
// unknown, e.g. a blank or an unprojected gameweek).
func (p *Player) PointsIn(gw int) float64 {
	if p.GWPoints == nil {
		return 0
	}
	return p.GWPoints[gw]
}

// roster is a set of players that make up a squad, keyed by player id.
type roster map[int]*Player

// byPosition splits a roster into per-position slices sorted by points in
// the given gameweek, descending.
func (r roster) byPosition(gw int) map[string][]*Player {
	out := map[string][]*Player{}
	for _, p := range r {
		out[p.Position] = append(out[p.Position], p)
	}
	for _, ps := range out {
		sort.Slice(ps, func(i, j int) bool {
			pi, pj := ps[i].PointsIn(gw), ps[j].PointsIn(gw)
			if pi != pj {
				return pi > pj
			}
			return ps[i].ID < ps[j].ID
		})
	}
	return out
}

// ValidateShape reports whether a squad has the mandatory 2/5/5/3 shape.
// Used as a guard before running the XI optimiser; a malformed squad is a
// data problem the caller should surface, not crash on.
func ValidateShape(players map[int]*Player) error {
	counts := map[string]int{}
	for _, p := range players {
		counts[p.Position]++
	}
	for pos, want := range SquadShape {
		if counts[pos] != want {
			return fmt.Errorf("squad has %d %s players, want %d", counts[pos], pos, want)
		}
	}
	return nil
}
