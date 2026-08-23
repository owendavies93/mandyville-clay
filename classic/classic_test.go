package classic

import (
	"math"
	"testing"

	"github.com/mandyville/mandyville-draft/squad"
)

func TestSellingPrice(t *testing.T) {
	cases := []struct {
		purchase, current, want int
	}{
		// No change: sell at purchase.
		{65, 65, 65},
		// Rise: half the profit, rounded down.
		{65, 71, 68}, // 65 + floor(6/2) = 68
		{65, 70, 67}, // 65 + floor(5/2) = 67
		// Fall: sell at current.
		{65, 60, 60},
		// Rise of a single tenth: profit 1 -> floor(1/2) = 0.
		{65, 66, 65},
	}
	for _, c := range cases {
		if got := SellingPrice(c.purchase, c.current); got != c.want {
			t.Errorf("SellingPrice(%d, %d) = %d, want %d", c.purchase, c.current, got, c.want)
		}
	}
}

func TestFreeTransferCount(t *testing.T) {
	// Helper: history rows with the given event transfers, a transfer row
	// list with the given events, and chips with the given (name, event).
	mk := func(historyEvents map[int]int, transferEvents []int, chipEvents map[string]int) ([]HistoryRow, []Transfer, []Chip) {
		var history []HistoryRow
		for e, n := range historyEvents {
			history = append(history, HistoryRow{Event: e, EventTransfers: n})
		}
		var transfers []Transfer
		for _, e := range transferEvents {
			transfers = append(transfers, Transfer{Event: e})
		}
		var chips []Chip
		for name, e := range chipEvents {
			chips = append(chips, Chip{Name: name, Event: e})
		}
		return history, transfers, chips
	}

	t.Run("after GW1 no transfers", func(t *testing.T) {
		// Pre-season is unlimited; you receive 1 FT after GW1.
		h, tr, ch := mk(map[int]int{1: 0}, nil, nil)
		if got := FreeTransferCount(h, tr, ch, 2); got != 1 {
			t.Fatalf("free transfers = %d, want 1", got)
		}
	})

	t.Run("roll after GW2", func(t *testing.T) {
		// GW2 used 0 of the 1 FT, so bank 1 + earn 1 = 2 for GW3.
		h, tr, ch := mk(map[int]int{1: 0, 2: 0}, nil, nil)
		if got := FreeTransferCount(h, tr, ch, 3); got != 2 {
			t.Fatalf("free transfers = %d, want 2", got)
		}
	})

	t.Run("hit consumes banked transfer", func(t *testing.T) {
		// After GW1: 1 FT. GW2 no transfers (bank 1+1=2). GW3 makes 3
		// transfers: uses both FTs and takes one hit, then earns 1 for GW4.
		h, tr, ch := mk(map[int]int{1: 0, 2: 0, 3: 3}, nil, nil)
		if got := FreeTransferCount(h, tr, ch, 4); got != 1 {
			t.Fatalf("free transfers = %d, want 1", got)
		}
	})

	t.Run("cap at five", func(t *testing.T) {
		// Seven completed gameweeks, no transfers -> capped at 5.
		h, tr, ch := mk(map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0, 6: 0, 7: 0}, nil, nil)
		if got := FreeTransferCount(h, tr, ch, 8); got != 5 {
			t.Fatalf("free transfers = %d, want 5", got)
		}
	})

	t.Run("wildcard resets banked FTs", func(t *testing.T) {
		// GW2 no transfers (bank 2). GW3 wildcard: resets to 1 FT for GW4.
		h, tr, ch := mk(map[int]int{1: 0, 2: 0, 3: 8}, nil, map[string]int{"wildcard": 3})
		if got := FreeTransferCount(h, tr, ch, 4); got != 1 {
			t.Fatalf("free transfers = %d, want 1", got)
		}
	})

	t.Run("free hit preserves banked FTs", func(t *testing.T) {
		// GW2 no transfers (bank 2). GW3 free hit: FTs unchanged, earn
		// 1 more for GW4 = 3.
		h, tr, ch := mk(map[int]int{1: 0, 2: 0, 3: 8}, nil, map[string]int{"freehit": 3})
		if got := FreeTransferCount(h, tr, ch, 4); got != 3 {
			t.Fatalf("free transfers = %d, want 3", got)
		}
	})

	t.Run("transfers already made for the upcoming GW", func(t *testing.T) {
		// After GW1: 1 FT. One transfer already made for GW2 consumes it.
		h, tr, ch := mk(map[int]int{1: 0}, []int{2}, nil)
		if got := FreeTransferCount(h, tr, ch, 2); got != 0 {
			t.Fatalf("free transfers = %d, want 0", got)
		}
	})
}

// testWorld builds a synthetic pool and squad for the planner tests:
// a 15-man squad of ids 1..15 (2 GK, 5 DEF, 5 MID, 3 FWD) plus two
// transfer targets X (element 16) and Y (element 17) at DEF. All prices are
// 50 tenths except where overridden, every player has a unique club, and
// the squad starts with bank 0 and one free transfer.
func testWorld(xGW2, yGW2 float64, xPrice, yPrice int) (*Pool, *Squad) {
	pool := &Pool{ByElement: map[int]*PoolPlayer{}, ByPlayer: map[int]int{}}
	mk := func(id int, pos string, gw1, gw2 float64, price int) {
		p := &squad.Player{ID: id, Position: pos,
			GWPoints: map[int]float64{1: gw1, 2: gw2}}
		pool.ByElement[id] = &PoolPlayer{
			Element: id, PlayerID: id, Player: p, TeamID: id, Price: price,
		}
		pool.ByPlayer[id] = id
	}

	mk(1, "GK", 2, 2, 50)
	mk(2, "GK", 2, 2, 50)
	mk(10, "DEF", 6, 6, 50)
	mk(11, "DEF", 5, 5, 50)
	mk(12, "DEF", 4, 4, 50)
	mk(13, "DEF", 2, 2, 50) // weak defender 4
	mk(14, "DEF", 1, 1, 50) // weak defender 5
	for i := 0; i < 5; i++ {
		mk(20+i, "MID", 3, 3, 50)
	}
	for i := 0; i < 3; i++ {
		mk(30+i, "FWD", 3, 3, 50)
	}
	mk(16, "DEF", 1, xGW2, xPrice) // target X
	mk(17, "DEF", 1, yGW2, yPrice) // target Y

	squad := &Squad{Members: map[int]*Member{}, Bank: 0, FreeTransfers: 1}
	for _, id := range []int{1, 2, 10, 11, 12, 13, 14, 20, 21, 22, 23, 24, 30, 31, 32} {
		pp := pool.ByElement[id]
		squad.Members[id] = &Member{
			Element: id, PlayerID: id, Player: pp.Player,
			Position: pp.Player.Position, TeamID: id,
			PurchasePrice: 50, CurrentPrice: 50,
		}
	}
	return pool, squad
}

func TestClubOK(t *testing.T) {
	roster := map[int]*Member{
		1: {Element: 1, TeamID: 5},
		2: {Element: 2, TeamID: 5},
		3: {Element: 3, TeamID: 5},
		4: {Element: 4, TeamID: 7},
	}
	if clubOK(roster, 4, &PoolPlayer{TeamID: 5}) {
		t.Error("expected club limit violation buying a 4th player from club 5")
	}
	if !clubOK(roster, 4, &PoolPlayer{TeamID: 7}) {
		t.Error("expected OK when selling the only member of club 7")
	}
	if !clubOK(roster, 4, &PoolPlayer{TeamID: 9}) {
		t.Error("expected OK for a fresh club")
	}
}

func TestPairClubOK(t *testing.T) {
	roster := map[int]*Member{
		1: {Element: 1, TeamID: 5},
		2: {Element: 2, TeamID: 5},
		3: {Element: 3, TeamID: 7},
		4: {Element: 4, TeamID: 8},
	}
	// Buying two from club 5 when it already has 2: each single is OK
	// (2+1=3), but together they'd reach 4.
	a := Move{OutElement: 3, In: &PoolPlayer{TeamID: 5}}
	b := Move{OutElement: 4, In: &PoolPlayer{TeamID: 5}}
	if pairClubOK(roster, a, b) {
		t.Error("expected pair club limit violation for two buys from the same club")
	}

	// One buy from club 5 and one from club 9: individually and jointly OK.
	c := Move{OutElement: 4, In: &PoolPlayer{TeamID: 9}}
	if !pairClubOK(roster, a, c) {
		t.Error("expected OK when pair buys from distinct clubs")
	}

	// Selling from club 5 and buying back into club 5 (swap): stays at 2.
	d := Move{OutElement: 1, In: &PoolPlayer{TeamID: 5}}
	if !pairClubOK(roster, d, c) {
		t.Error("expected OK when selling and buying within same club")
	}
}

func TestPlannerRollsInsteadOfHit(t *testing.T) {
	// Targets X and Y only improve the XI in gameweek 2. Doing both
	// transfers this week costs a -4 hit (only 1 FT); rolling this week
	// banks a second FT and does both for free in GW2. Both reach the same
	// GW2 XI, so the planner must avoid the hit.
	pool, squad := testWorld(8, 7, 50, 45)
	planner := NewPlanner(pool, 1, 2, 20, 2, 30)

	out, err := planner.Plan(squad)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Best plans reach 44 (GW1) + 55 (GW2 with both targets, incl. captain)
	// = 99, with no hit this week (a hit would drop the total to 95).
	if math.Abs(out.Recommended.Total-99) > 1e-6 {
		t.Fatalf("recommended total = %.1f, want 99", out.Recommended.Total)
	}
	if len(out.Recommended.Immediate) > 1 {
		t.Fatalf("recommended immediate transfers = %d, want <= 1 (no hit this week)", len(out.Recommended.Immediate))
	}
	if out.RollThisWeek == nil || math.Abs(out.RollThisWeek.Total-99) > 1e-6 {
		t.Fatalf("roll-this-week total = %v, want 99", rollTotal(out.RollThisWeek))
	}
}

func rollTotal(p *Plan) float64 {
	if p == nil {
		return -1
	}
	return p.Total
}

func TestPlannerRespectsBudget(t *testing.T) {
	// X is unaffordable (200 tenths) with bank 0; only the cheaper Y can be
	// bought. The planner must not reach the 47-point two-target XI.
	pool, squad := testWorld(8, 7, 200, 45)
	planner := NewPlanner(pool, 1, 2, 20, 2, 30)

	out, err := planner.Plan(squad)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// With only Y available, the GW2 XI gains at most the Y swap, so the
	// total stays below the 99 ceiling that needs both X and Y.
	if out.Recommended.Total >= 99-1e-6 {
		t.Fatalf("recommended total = %.1f, want < 99 (X is unaffordable)", out.Recommended.Total)
	}
}
