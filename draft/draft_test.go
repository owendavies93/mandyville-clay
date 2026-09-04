package draft

import (
	"math"
	"testing"

	"github.com/mandyville/mandyville-draft/squad"
)

// p is a test helper: a player with the same points value in gameweeks
// 5, 6 and 7, so both single-gameweek and horizon tests can use it.
func p(id int, name, pos string, pts float64) *Player {
	return &Player{ID: id, Name: name, Position: pos,
		GWPoints: map[int]float64{5: pts, 6: pts, 7: pts}}
}

// makeSquad builds a full 2/5/5/3 squad around a single configurable
// "focus" player, so swap tests can exercise one drop slot. All points are
// for gameweek 5.
func makeSquad(focus *Player, weakDefs int) map[int]*Player {
	defs := []float64{6, 5, 4, 3, 2}
	mids := []float64{6, 5, 4, 3, 2}
	fwds := []float64{6, 5, 4}
	gks := []float64{5, 4}

	squad := map[int]*Player{
		1: p(1, "gk1", PosGK, gks[0]),
		2: p(2, "gk2", PosGK, gks[1]),
	}
	for i, v := range mids {
		squad[20+i] = p(20+i, "m", PosMID, v)
	}
	for i, v := range fwds {
		squad[30+i] = p(30+i, "f", PosFWD, v)
	}
	for i, v := range defs {
		squad[10+i] = p(10+i, "d", PosDEF, v)
	}
	// Replace `weakDefs` defenders with near-zero points so swapping them
	// out is clearly valuable.
	for i := 0; i < weakDefs; i++ {
		squad[10+i] = p(10+i, "dweak", PosDEF, 0.1)
	}

	if focus != nil {
		// Overwrite the weakest defender slot with the focus player.
		squad[10+weakDefs-1] = focus
	}
	return squad
}

func TestBestXI(t *testing.T) {
	squad := makeSquad(nil, 0)
	sel, total := BestXI(squad, 5)

	if len(sel) != 11 {
		t.Fatalf("BestXI returned %d players, want 11", len(sel))
	}
	// Best formation is {3,4,3} (or a 53-point tie); either way the total
	// is 5 (GK) + 6+5+4 (DEF) + 6+5+4+3 (MID) + 6+5+4 (FWD) = 53.
	if math.Abs(total-53) > 1e-9 {
		t.Fatalf("BestXI total = %.1f, want 53", total)
	}

	contains := func(id int) bool {
		for _, s := range sel {
			if s == id {
				return true
			}
		}
		return false
	}
	// {3,4,3} starts the 4th midfielder (id 23) and benches the 4th
	// defender (id 13).
	if !contains(23) {
		t.Errorf("expected 4th midfielder to start under {3,4,3}")
	}
	if contains(13) {
		t.Errorf("did not expect 4th defender to start under {3,4,3}")
	}
}

func TestSwapValueBenchPlayer(t *testing.T) {
	// The 5th defender (d5, 2 pts) is a bench player. Swapping it for an
	// 8-point defender shifts the best formation from {3,4,3} (53 pts) to
	// {4,3,3} (58 pts): the 4th defender (4) now displaces the 4th
	// midfielder (3), so the XI gains 5, not the naive 8 - 2 = 6.
	squad := makeSquad(nil, 0)
	out := squad[14] // d5, 2 pts
	in := p(99, "new", PosDEF, 8)

	perGW, discounted := SwapValue(squad, out, in, 5, 1, 1.0)
	if math.Abs(perGW[0]-5) > 1e-9 {
		t.Fatalf("SwapValue per-GW = %.1f, want 5", perGW[0])
	}
	if math.Abs(discounted-5) > 1e-9 {
		t.Fatalf("SwapValue discounted = %.1f, want 5", discounted)
	}
}

func TestSwapValueDiscounting(t *testing.T) {
	squad := makeSquad(nil, 0)
	out := squad[14] // d5, 2 pts
	in := p(99, "new", PosDEF, 8)

	perGW, discounted := SwapValue(squad, out, in, 5, 3, 0.5)
	// Gain is 5 every gameweek (same fixture-independent points).
	for i, g := range perGW {
		if math.Abs(g-5) > 1e-9 {
			t.Fatalf("perGW[%d] = %.1f, want 5", i, g)
		}
	}
	want := 5*1 + 5*0.5 + 5*0.25 // 8.75
	if math.Abs(discounted-want) > 1e-9 {
		t.Fatalf("discounted = %.3f, want %.3f", discounted, want)
	}
}

func TestEvaluateSwapsRespectsPosition(t *testing.T) {
	squad := makeSquad(nil, 1) // weakest DEF has 0.1 pts
	freeAgents := map[int]*Player{
		99: p(99, "newdef", PosDEF, 8),
		98: p(98, "newfwd", PosFWD, 8),
	}

	cands := EvaluateSwaps(squad, freeAgents, nil, 5, 1, 1.0)
	// 5 defenders and 3 forwards are all worse than the 8-point free
	// agents, so every same-position swap is a candidate; cross-position
	// swaps must never appear.
	if len(cands) != 8 {
		t.Fatalf("got %d candidates, want 8 (5 DEF + 3 FWD)", len(cands))
	}
	for _, c := range cands {
		if c.In.Position != c.Out.Position {
			t.Errorf("cross-position swap: %s out vs %s in", c.Out.Position, c.In.Position)
		}
	}
}

// TestEvaluateSwapsSurfacesDeadSlots covers the case that a horizon-bound
// marginal-XI gain cannot see: a player who has left the league sits on the
// bench projecting nothing, so dropping him gains nothing over the horizon
// even though his slot is worthless for the whole season.
func TestEvaluateSwapsSurfacesDeadSlots(t *testing.T) {
	// A full squad of strong forwards, plus a fourth-choice FWD who is
	// gone. He never makes the XI, so no swap for him can gain anything.
	squad := makeSquad(nil, 0)
	gone := p(31, "gone", PosFWD, 0)
	squad[31] = gone

	// The replacement is a bench forward too: better than nothing over the
	// season, but not good enough to crack the XI over the horizon.
	freeAgents := map[int]*Player{
		99: {ID: 99, Name: "replacement", Position: PosFWD,
			GWPoints: map[int]float64{5: 0, 6: 0, 7: 0, 8: 40}},
	}

	if cands := EvaluateSwaps(squad, freeAgents, nil, 5, 3, 1.0); len(cands) != 0 {
		t.Fatalf("without a dead set, got %d candidates, want 0", len(cands))
	}

	cands := EvaluateSwaps(squad, freeAgents, map[int]bool{31: true}, 5, 3, 1.0)
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1", len(cands))
	}
	c := cands[0]
	if !c.DeadSlot {
		t.Error("candidate not marked as a dead slot")
	}
	if math.Abs(c.Gain) > 1e-9 {
		t.Errorf("Gain = %.3f, want 0 (the horizon cannot see the cost)", c.Gain)
	}
	if math.Abs(c.OutROS) > 1e-9 {
		t.Errorf("OutROS = %.3f, want 0", c.OutROS)
	}
	if math.Abs(c.ROSGain()-40) > 1e-9 {
		t.Errorf("ROSGain = %.3f, want 40", c.ROSGain())
	}
}

func TestClaimProbabilities(t *testing.T) {
	model := DefaultWaiverModel()
	model.Iterations = 2000
	model.Seed = 42
	model.HoldProb = 0
	model.Tau = 1.0
	model.MinValue = 0.5
	model.MaxCandidates = 5

	// A rival ahead of me has a terrible 5th defender and will usually
	// claim the 10-point free agent F, sometimes the 9-point G, never H.
	rivalRoster := makeSquad(p(10+0, "dweak", PosDEF, 0.1), 1)
	rivals := []RivalSquad{{Entry: Entry{ID: 7}, Roster: rivalRoster}}

	freeAgents := map[int]*Player{
		99: p(99, "F", PosDEF, 10),
		98: p(98, "G", PosDEF, 9),
		97: p(97, "H", PosDEF, 0.1),
	}

	order := []WaiverOrder{{EntryID: 7, WaiverPick: 1}, {EntryID: 5, WaiverPick: 2}}
	probs := ClaimProbabilities(model, order, 5, rivals, freeAgents, 5, 1, 1.0)

	if probs[97] != 1.0 {
		t.Errorf("P(H survives) = %.3f, want 1.0 (nobody wants H)", probs[97])
	}
	if probs[99] >= 0.7 || probs[99] <= 0.0 {
		t.Errorf("P(F survives) = %.3f, want in (0, 0.7)", probs[99])
	}
	if probs[98] <= 0.0 || probs[98] >= 1.0 {
		t.Errorf("P(G survives) = %.3f, want in (0, 1)", probs[98])
	}
}

func TestH2HGain(t *testing.T) {
	// A more consistent incoming player should improve the H2H figure.
	out := p(1, "out", PosMID, 5)
	out.Consistency = 4.0
	in := p(2, "in", PosMID, 5)
	in.Consistency = 2.0

	// horizon 1: penalty = 2*1*(2-4) = -4, so h2hGain = gain + 4.
	if got := h2hGain(10, 1, out, in); math.Abs(got-14) > 1e-9 {
		t.Fatalf("h2hGain(10, 1) = %.2f, want 14", got)
	}
	// A more volatile incoming player drags it down: horizon 4 penalty is
	// 2*sqrt(4)*(4-2) = 8, so h2hGain = 10 - 8 = 2.
	if got := h2hGain(10, 4, in, out); math.Abs(got-2) > 1e-9 {
		t.Fatalf("h2hGain(10, 4) = %.2f, want 2", got)
	}
}

func TestRosterValidateShape(t *testing.T) {
	valid := makeSquad(nil, 0)
	if err := squad.ValidateShape(valid); err != nil {
		t.Fatalf("valid roster failed validation: %v", err)
	}

	delete(valid, 14) // remove a defender
	if err := squad.ValidateShape(valid); err == nil {
		t.Fatal("malformed roster passed validation")
	}
}
