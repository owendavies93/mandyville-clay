// Package draft loads FPL Draft league state from the mandyville database
// and turns it into transfer, waiver and starting-XI recommendations.
//
// It is deliberately independent of the projection package's output types:
// callers convert projections into Player values, which keeps the XI
// optimiser and waiver simulation pure and unit-testable.
package draft

import (
	"github.com/mandyville/mandyville-draft/squad"
)

// Position constants and the shared squad types are re-exported from the
// squad package so draft callers keep a single import.
const (
	PosGK  = squad.PosGK
	PosDEF = squad.PosDEF
	PosMID = squad.PosMID
	PosFWD = squad.PosFWD
)

// SquadShape is the fixed FPL Draft squad composition.
var SquadShape = squad.SquadShape

// Player is a projectable player, re-exported from the squad package.
type Player = squad.Player

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
