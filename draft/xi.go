package draft

import (
	"math"
	"sort"
)

// formation is an outfield shape for the starting XI (DEF-MID-FWD). The
// goalkeeper is always exactly one, so it is not part of the formation.
type formation struct{ def, mid, fwd int }

// formations lists every legal outfield shape: 3-5 DEF, 2-5 MID, 1-3 FWD,
// summing to 10. Sourced from the FPL Draft squad settings.
var formations = []formation{
	{3, 4, 3}, {3, 5, 2}, {4, 3, 3}, {4, 4, 2},
	{4, 5, 1}, {5, 2, 3}, {5, 3, 2}, {5, 4, 1},
}

// BestXI returns the highest-scoring legal starting XI for a single
// gameweek: the selected player ids (11 of them) and the total projected
// points. For a fixed formation the best XI is greedy by position, so the
// optimiser just maximises over formations.
func BestXI(players map[int]*Player, gw int) ([]int, float64) {
	r := roster(players)
	byPos := r.byPosition(gw)

	gks := byPos[PosGK]
	if len(gks) == 0 {
		return nil, 0
	}
	bestGK := gks[0]

	defs, mids, fwds := byPos[PosDEF], byPos[PosMID], byPos[PosFWD]

	best := math.Inf(-1)
	var bestSel []int
	for _, f := range formations {
		if len(defs) < f.def || len(mids) < f.mid || len(fwds) < f.fwd {
			continue
		}

		sel := make([]int, 0, 11)
		sel = append(sel, bestGK.ID)
		total := bestGK.pointsIn(gw)

		for _, p := range defs[:f.def] {
			sel = append(sel, p.ID)
			total += p.pointsIn(gw)
		}
		for _, p := range mids[:f.mid] {
			sel = append(sel, p.ID)
			total += p.pointsIn(gw)
		}
		for _, p := range fwds[:f.fwd] {
			sel = append(sel, p.ID)
			total += p.pointsIn(gw)
		}

		if total > best {
			best = total
			bestSel = sel
		}
	}

	if bestSel == nil {
		// No formation fits (malformed squad). Fall back to the best
		// eleven players regardless of legality so callers still get a
		// number rather than -Inf.
		all := make([]*Player, 0, len(players))
		for _, p := range players {
			all = append(all, p)
		}
		sort.Slice(all, func(i, j int) bool {
			return all[i].pointsIn(gw) > all[j].pointsIn(gw)
		})
		n := 11
		if len(all) < n {
			n = len(all)
		}
		total := 0.0
		for _, p := range all[:n] {
			bestSel = append(bestSel, p.ID)
			total += p.pointsIn(gw)
		}
		return bestSel, total
	}

	return bestSel, best
}

// SwapValue computes the marginal starting-XI value of replacing `out` with
// `in` over the horizon [startGW, startGW+horizon). Both must be the same
// position. It returns the per-gameweek XI gain (index 0 is startGW) and
// the geometrically-discounted total.
func SwapValue(players map[int]*Player, out, in *Player, startGW, horizon int, discount float64) ([]float64, float64) {
	swapped := make(roster, len(players))
	for id, p := range players {
		if id != out.ID {
			swapped[id] = p
		}
	}
	swapped[in.ID] = in

	base := roster(players)
	perGW := make([]float64, 0, horizon)
	discounted := 0.0
	for i := 0; i < horizon; i++ {
		gw := startGW + i
		_, basePts := BestXI(base, gw)
		_, newPts := BestXI(swapped, gw)
		gain := newPts - basePts
		perGW = append(perGW, gain)
		discounted += gain * math.Pow(discount, float64(i))
	}
	return perGW, discounted
}
