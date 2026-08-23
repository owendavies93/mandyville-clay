package squad

import (
	"math"
	"testing"
)

func p(id int, name, pos string, pts float64) *Player {
	return &Player{ID: id, Name: name, Position: pos,
		GWPoints: map[int]float64{5: pts, 6: pts, 7: pts}}
}

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
	for i := 0; i < weakDefs; i++ {
		squad[10+i] = p(10+i, "dweak", PosDEF, 0.1)
	}
	if focus != nil {
		squad[10+weakDefs-1] = focus
	}
	return squad
}

func TestBestXIWithCaptain(t *testing.T) {
	squad := makeSquad(nil, 0)
	// XI is {3,4,3}: 5 (GK) + 6+5+4 (DEF) + 6+5+4+3 (MID) + 6+5+4 (FWD) = 53.
	// The captain is the highest-projected player: the 6-point players
	// (ids 10, 20, 30), any of which doubles to add 6.
	xi, captain, total := BestXIWithCaptain(squad, 5)
	if len(xi) != 11 {
		t.Fatalf("BestXIWithCaptain returned %d players, want 11", len(xi))
	}
	if math.Abs(total-59) > 1e-9 {
		t.Fatalf("BestXIWithCaptain total = %.1f, want 59", total)
	}
	if c := squad[captain].PointsIn(5); math.Abs(c-6) > 1e-9 {
		t.Fatalf("captain has %.1f points, want 6", c)
	}
}

func TestBestXIWithCaptainTopScorerIsForward(t *testing.T) {
	// The global top scorer is a 40-point forward; the captain must be
	// them and the doubled points added regardless of formation.
	squad := makeSquad(nil, 0)
	squad[39] = p(39, "star", PosFWD, 40)

	xi, captain, total := BestXIWithCaptain(squad, 5)
	if captain != 39 {
		t.Fatalf("captain = %d, want 39", captain)
	}
	// Recompute expected XI sum: replacing fwd3 (4 pts) with 40 shifts the
	// FWD block from 6+5+4=15 to 40+6+5=51, so XI total = 5 + 15 + 18 + 51
	// = 89 under {3,4,3}. Captain adds 40 -> 129.
	_ = xi
	if math.Abs(total-129) > 1e-9 {
		t.Fatalf("BestXIWithCaptain total = %.1f, want 129", total)
	}
}

func TestBenchOrder(t *testing.T) {
	squad := makeSquad(nil, 0)
	xi, _ := BestXI(squad, 5)
	bench := BenchOrder(squad, xi, 5)

	// Bench is 4 players: reserve GK first, then outfield by points desc.
	if len(bench) != 4 {
		t.Fatalf("bench has %d players, want 4", len(bench))
	}
	if squad[bench[0]].Position != PosGK {
		t.Fatalf("bench[0] is %s, want the reserve GK", squad[bench[0]].Position)
	}
	for i := 1; i < len(bench); i++ {
		a, b := squad[bench[i]].PointsIn(5), squad[bench[i-1]].PointsIn(5)
		if i > 1 && a > b {
			t.Fatalf("outfield bench not sorted descending at %d", i)
		}
	}
}
