package draft

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Candidate is a single same-position swap: drop Out for the free agent In.
type Candidate struct {
	Out          *Player
	In           *Player
	Position     string
	PerGW        []float64 // per-gameweek XI gain (index 0 = startGW)
	Gain         float64   // discounted total gain
	Undiscounted float64
	H2HGain      float64  // discounted gain, adjusted for consistency
	SuccessProb  *float64 // P(target survives to my turn); nil for free agents
	ClaimOrder   int      // 1-based position within its drop group, 0 if unset
}

// EvaluateSwaps evaluates every same-position swap of a rostered player for
// a free agent, returning candidates ranked by discounted gain (descending).
// Only free agents with a non-empty projection are considered; the caller is
// responsible for surfacing unmatched elements separately.
func EvaluateSwaps(roster, freeAgents map[int]*Player, startGW, horizon int, discount float64) []Candidate {
	var out []Candidate
	for _, drop := range roster {
		for _, add := range freeAgents {
			if add.Position != drop.Position {
				continue
			}
			perGW, gain := SwapValue(roster, drop, add, startGW, horizon, discount)
			var undiscounted float64
			for _, g := range perGW {
				undiscounted += g
			}
			c := Candidate{
				Out:          drop,
				In:           add,
				Position:     drop.Position,
				PerGW:        perGW,
				Gain:         gain,
				Undiscounted: undiscounted,
				H2HGain:      h2hGain(gain, horizon, drop, add),
			}
			if c.Gain > 0 {
				out = append(out, c)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Gain > out[j].Gain
	})
	return out
}

// h2hGain adjusts a swap's expected gain for the consistency difference
// between the incoming and outgoing player. Over H gameweeks the standard
// deviation of the summed points scales by sqrt(H), so the engine's
// `points - 2*stddev` convention becomes a 2*sqrt(H)*(stddev_in - stddev_out)
// penalty on the gain.
func h2hGain(gain float64, horizon int, out, in *Player) float64 {
	if horizon <= 0 {
		return gain
	}
	penalty := 2 * math.Sqrt(float64(horizon)) * (in.Consistency - out.Consistency)
	return gain - penalty
}

// WaiverModel controls the rival-claim simulation. Rivals are modelled
// symmetrically to us: each claims probabilistically via a softmax over
// their best available upgrades.
type WaiverModel struct {
	Tau           float64 // softmax temperature (points)
	HoldProb      float64 // chance a rival makes no claim despite options
	MaxCandidates int     // top K rivals consider
	MinValue      float64 // below this a rival ignores a swap
	Iterations    int     // monte-carlo iterations
	Seed          int64   // 0 = nondeterministic
}

// DefaultWaiverModel returns the tuned defaults. The constants are
// deliberately simple and can be revisited once logged recommendations give
// us a grading signal.
func DefaultWaiverModel() WaiverModel {
	return WaiverModel{
		Tau:           2.0,
		HoldProb:      0.2,
		MaxCandidates: 5,
		MinValue:      0.5,
		Iterations:    200,
		Seed:          0,
	}
}

// RivalSquad is another entry's roster, used by the waiver simulation.
type RivalSquad struct {
	Entry  Entry
	Roster map[int]*Player
}

// ClaimProbabilities estimates, for every free agent, the probability it is
// still available when my turn in the waiver order arrives. Rivals ahead of
// me are processed in waiver order and claim stochastically; once I'm
// reached the surviving set is tallied. The result is keyed by player id.
func ClaimProbabilities(model WaiverModel, order []WaiverOrder, myEntryID int, rivals []RivalSquad, freeAgents map[int]*Player, startGW, horizon int, discount float64) map[int]float64 {
	rivalCands := make(map[int][]Candidate, len(rivals))
	for _, r := range rivals {
		cands := EvaluateSwaps(r.Roster, freeAgents, startGW, horizon, discount)
		cands = filterCandidates(cands, model.MinValue)
		if len(cands) > model.MaxCandidates {
			cands = cands[:model.MaxCandidates]
		}
		rivalCands[r.Entry.ID] = cands
	}

	sorted := make([]WaiverOrder, len(order))
	copy(sorted, order)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].WaiverPick < sorted[j].WaiverPick
	})

	rng := rand.New(rand.NewPCG(uint64(model.Seed), uint64(model.Seed)^0x9e3779b97f4a7c15))

	// freeID is the ordered list of free-agent player ids we track.
	freeID := make([]int, 0, len(freeAgents))
	for id := range freeAgents {
		freeID = append(freeID, id)
	}

	survived := make(map[int]int, len(freeID))
	for iter := 0; iter < model.Iterations; iter++ {
		available := make(map[int]bool, len(freeID))
		for _, id := range freeID {
			available[id] = true
		}

		tallied := false
		for _, w := range sorted {
			if w.EntryID == myEntryID {
				for id := range available {
					survived[id]++
				}
				tallied = true
				break
			}

			cands := rivalCands[w.EntryID]
			if len(cands) == 0 || rng.Float64() < model.HoldProb {
				continue
			}

			// Keep only swaps whose target is still available, then pick
			// one via softmax over their values.
			var pick *Candidate
			var weights []float64
			var filtered []int
			for ci, c := range cands {
				if available[c.In.ID] {
					filtered = append(filtered, ci)
					weights = append(weights, math.Exp(c.Gain/model.Tau))
				}
			}
			if len(filtered) == 0 {
				continue
			}

			sum := 0.0
			for _, wgt := range weights {
				sum += wgt
			}
			r := rng.Float64() * sum
			for k, idx := range filtered {
				r -= weights[k]
				if r <= 0 {
					pick = &cands[idx]
					break
				}
			}
			if pick == nil {
				pick = &cands[filtered[len(filtered)-1]]
			}
			delete(available, pick.In.ID)
		}

		// If my entry has no waiver-order row, assume I'm last and tally
		// whatever survives the rivals ahead of me.
		if !tallied {
			for id := range available {
				survived[id]++
			}
		}
	}

	probs := make(map[int]float64, len(freeID))
	for id, n := range survived {
		probs[id] = float64(n) / float64(model.Iterations)
	}
	return probs
}

// filterCandidates drops candidates below a minimum gain, preserving order.
func filterCandidates(cands []Candidate, minValue float64) []Candidate {
	out := cands[:0]
	for _, c := range cands {
		if c.Gain >= minValue {
			out = append(out, c)
		}
	}
	return out
}
