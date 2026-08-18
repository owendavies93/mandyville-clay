// Package draft loads FPL Draft league state from the mandyville database
// and turns it into transfer, waiver and starting-XI recommendations.
//
// It is deliberately independent of the projection package's output types:
// callers convert projections into Player values, which keeps the XI
// optimiser and waiver simulation pure and unit-testable.
package draft

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

// SquadShape is the fixed FPL Draft squad composition. Because it never
// changes, every legal transfer is a same-position swap.
var SquadShape = map[string]int{PosGK: 2, PosDEF: 5, PosMID: 5, PosFWD: 3}

// League is a row from fpl_draft_leagues.
type League struct {
	ID              int // internal database id
	FPLID           int // FPL draft league id
	Season          int
	Name            string
	Scoring         string
	TransactionMode string
	Trades          string
	StartEvent      int
	StopEvent       int
}

// Entry is a manager in a draft league (a row from fpl_draft_entries).
type Entry struct {
	ID            int // internal database id
	LeagueID      int
	EntryID       int // global FPL entry id
	LeagueEntryID int
	Name          string
	ShortName     string
	IsMine        bool
}

// Ownership is an open fpl_draft_ownership row. EntryID 0 means the player
// is a free agent; PlayerID 0 means the draft element has not been matched
// to a mandyville player yet.
type Ownership struct {
	LeagueID int
	EntryID  int
	PlayerID int
	Element  int
	Status   string
	InTrade  bool
}

// WaiverOrder is an open fpl_draft_waiver_order row.
type WaiverOrder struct {
	EntryID    int
	WaiverPick int
}

// PickSlot is a single row from fpl_draft_entry_picks: a player's position
// in an entry's squad for a gameweek (1-15, with 1-11 starting).
type PickSlot struct {
	PlayerID   int
	Element    int
	Position   int
	IsStarting bool
}

// Player is a projectable player: identity, position and projected points
// per gameweek, plus the engine's consistency metric for the H2H view.
type Player struct {
	ID          int
	Name        string
	Position    string
	GWPoints    map[int]float64 // gameweek -> projected points
	Consistency float64         // stddev of historic gameweek points
}

// pointsIn returns the player's projected points for a gameweek (0 if
// unknown, e.g. a blank or an unprojected gameweek).
func (p *Player) pointsIn(gw int) float64 {
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
			return ps[i].pointsIn(gw) > ps[j].pointsIn(gw)
		})
	}
	return out
}

// validateShape reports whether a roster has the mandatory 2/5/5/3 shape.
// Used as a guard before running the XI optimiser; a malformed squad is a
// data problem the caller should surface, not crash on.
func (r roster) validateShape() error {
	counts := map[string]int{}
	for _, p := range r {
		counts[p.Position]++
	}
	for pos, want := range SquadShape {
		if counts[pos] != want {
			return fmt.Errorf("roster has %d %s players, want %d", counts[pos], pos, want)
		}
	}
	return nil
}
