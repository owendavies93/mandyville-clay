// Package classic loads FPL Classic entry state from the mandyville
// database and plans budget-constrained transfers over a multi-gameweek
// horizon.
//
// All money is handled in integer tenths of millions (£0.1m units) to keep
// budget arithmetic exact: a player priced £6.5m is 65 tenths, a £100.0m
// budget is 1000 tenths.
package classic

import (
	"time"

	"github.com/mandyville/mandyville-draft/squad"
)

// Entry is a row from fpl_classic_entries.
type Entry struct {
	ID           int // internal database id
	FPLID        int // global FPL entry id
	Season       int
	Name         string
	StartedEvent int
}

// HistoryRow is a row from fpl_classic_entry_history.
type HistoryRow struct {
	Event              int
	Points             int
	TotalPoints        int
	Rank               int
	OverallRank        int
	Bank               int // tenths
	Value              int // tenths (team value)
	EventTransfers     int
	EventTransfersCost int
	PointsOnBench      int
}

// Transfer is a row from fpl_classic_transfers.
type Transfer struct {
	Event          int
	PlayerInID     int // mandyville player id (0 if unmatched)
	PlayerOutID    int
	ElementIn      int
	ElementOut     int
	ElementInCost  int // tenths (purchase price)
	ElementOutCost int // tenths (selling price)
	Time           time.Time
}

// Chip is a row from fpl_classic_chips.
type Chip struct {
	Name  string // "wildcard", "freehit", "bboost", "3xc"
	Event int
}

// Member is a player in the current squad, keyed by their classic FPL
// element id. PlayerID is 0 and Player nil when the element has not been
// matched to a mandyville player or has no projection.
type Member struct {
	Element       int
	PlayerID      int           // 0 if unmatched
	Player        *squad.Player // nil if unprojected
	Position      string        // "GK"/"DEF"/"MID"/"FWD" ("" if unknown)
	Name          string        // display name (filled even for unprojected players)
	TeamID        int           // 0 if unknown (unmatched players)
	PurchasePrice int           // tenths
	CurrentPrice  int           // tenths
}

// SellingPrice returns the amount received for selling a player bought at
// purchase tenths when the current price is current tenths. Gains are
// halved (rounded down); falls are passed through at the current price.
func SellingPrice(purchase, current int) int {
	if current > purchase {
		return purchase + (current-purchase)/2
	}
	return current
}

// Squad is the reconstructed state of the entry before the upcoming
// gameweek.
type Squad struct {
	Members       map[int]*Member // keyed by classic element id
	Captain       int             // classic element id, 0 if unknown
	ViceCaptain   int
	Bank          int // tenths
	FreeTransfers int
	Warnings      []string
}

// PoolPlayer is a transferable player: projection, club and current price,
// keyed by classic element id.
type PoolPlayer struct {
	Element  int
	PlayerID int // 0 if unmatched
	Player   *squad.Player
	TeamID   int
	TeamName string
	Price    int // tenths
}

// Pool is the set of all transferable players, keyed by classic element id.
type Pool struct {
	ByElement map[int]*PoolPlayer
	ByPlayer  map[int]int // mandyville player id -> classic element id
}
