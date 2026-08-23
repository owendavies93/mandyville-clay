package classic

import (
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/lib/pq"
	"github.com/mandyville/mandyville-draft/projection"
	"github.com/mandyville/mandyville-draft/squad"
)

// positionCode maps the database's position_category enum to the engine's
// position strings.
var positionCode = map[string]string{
	"Goalkeeper": squad.PosGK,
	"Defender":   squad.PosDEF,
	"Midfielder": squad.PosMID,
	"Forward":    squad.PosFWD,
}

// UpcomingGameweek returns the first gameweek whose deadline is in the
// future.
func UpcomingGameweek(db *sql.DB, season int) (int, error) {
	deadlines, err := projection.LoadGameweekDeadlines(db, season)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for gw := 1; gw <= 38; gw++ {
		if d, ok := deadlines[gw]; ok && d.After(now) {
			return gw, nil
		}
	}
	return 0, fmt.Errorf("no upcoming gameweek found for season %d (is the season over?)", season)
}

// LoadEntry resolves the classic entry for a season: the is_mine row by
// default, or the given FPL entry id when override is non-zero.
func LoadEntry(db *sql.DB, season, override int) (*Entry, error) {
	var (
		id           int
		fplID        int
		name         string
		startedEvent int
	)

	if override != 0 {
		err := db.QueryRow(`
			SELECT id, fpl_entry_id, entry_name, started_event
			FROM fpl_classic_entries WHERE fpl_entry_id = $1 AND season = $2`,
			override, season).Scan(&id, &fplID, &name, &startedEvent)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no classic entry with fpl entry id %d for season %d", override, season)
		}
		if err != nil {
			return nil, fmt.Errorf("loading classic entry: %w", err)
		}
		return &Entry{ID: id, FPLID: fplID, Season: season, Name: name, StartedEvent: startedEvent}, nil
	}

	rows, err := db.Query(`
		SELECT id, fpl_entry_id, entry_name, started_event
		FROM fpl_classic_entries WHERE season = $1 AND is_mine`, season)
	if err != nil {
		return nil, fmt.Errorf("loading classic entry: %w", err)
	}
	defer rows.Close()

	var found *Entry
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &fplID, &name, &startedEvent); err != nil {
			return nil, err
		}
		found = &Entry{ID: id, FPLID: fplID, Season: season, Name: name, StartedEvent: startedEvent}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch count {
	case 0:
		return nil, fmt.Errorf("no classic entry for season %d is marked is_mine", season)
	case 1:
		return found, nil
	default:
		return nil, fmt.Errorf("%d classic entries for season %d are marked is_mine; use -entry to disambiguate", count, season)
	}
}

// LoadHistory returns the per-gameweek history rows for an entry, ordered
// by event.
func LoadHistory(db *sql.DB, entryID int) ([]HistoryRow, error) {
	rows, err := db.Query(`
		SELECT event, points, total_points, rank, overall_rank, bank, value,
		       event_transfers, event_transfers_cost, points_on_bench
		FROM fpl_classic_entry_history
		WHERE classic_entry_id = $1 ORDER BY event`, entryID)
	if err != nil {
		return nil, fmt.Errorf("loading classic history: %w", err)
	}
	defer rows.Close()

	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		var points, rank, overallRank, eventTransfers, eventTransfersCost, pointsOnBench sql.NullInt64
		if err := rows.Scan(&h.Event, &points, &h.TotalPoints, &rank, &overallRank,
			&h.Bank, &h.Value, &eventTransfers, &eventTransfersCost, &pointsOnBench); err != nil {
			return nil, err
		}
		h.Points = int(points.Int64)
		h.Rank = int(rank.Int64)
		h.OverallRank = int(overallRank.Int64)
		h.EventTransfers = int(eventTransfers.Int64)
		h.EventTransfersCost = int(eventTransfersCost.Int64)
		h.PointsOnBench = int(pointsOnBench.Int64)
		out = append(out, h)
	}
	return out, rows.Err()
}

// LoadTransfers returns the transfer history for an entry, ordered by
// transfer time.
func LoadTransfers(db *sql.DB, entryID int) ([]Transfer, error) {
	rows, err := db.Query(`
		SELECT event, player_in_id, player_out_id, element_in, element_out,
		       element_in_cost, element_out_cost, transfer_time
		FROM fpl_classic_transfers
		WHERE classic_entry_id = $1 ORDER BY transfer_time`, entryID)
	if err != nil {
		return nil, fmt.Errorf("loading classic transfers: %w", err)
	}
	defer rows.Close()

	var out []Transfer
	for rows.Next() {
		var t Transfer
		var inID, outID, inCost, outCost sql.NullInt64
		if err := rows.Scan(&t.Event, &inID, &outID, &t.ElementIn, &t.ElementOut,
			&inCost, &outCost, &t.Time); err != nil {
			return nil, err
		}
		t.PlayerInID = int(inID.Int64)
		t.PlayerOutID = int(outID.Int64)
		t.ElementInCost = int(inCost.Int64)
		t.ElementOutCost = int(outCost.Int64)
		out = append(out, t)
	}
	return out, rows.Err()
}

// LoadChips returns the chips played by an entry.
func LoadChips(db *sql.DB, entryID int) ([]Chip, error) {
	rows, err := db.Query(`
		SELECT name, event FROM fpl_classic_chips
		WHERE classic_entry_id = $1 ORDER BY event`, entryID)
	if err != nil {
		return nil, fmt.Errorf("loading classic chips: %w", err)
	}
	defer rows.Close()

	var out []Chip
	for rows.Next() {
		var c Chip
		if err := rows.Scan(&c.Name, &c.Event); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LoadElementMapping returns the player_id <-> classic element id mapping
// for a season, from fpl_season_info.
func LoadElementMapping(db *sql.DB, season int) (byPlayer, byElement map[int]int, err error) {
	rows, err := db.Query(`
		SELECT player_id, fpl_season_id FROM fpl_season_info
		WHERE season = $1 AND fpl_season_id IS NOT NULL`, season)
	if err != nil {
		return nil, nil, fmt.Errorf("loading element mapping: %w", err)
	}
	defer rows.Close()

	byPlayer = map[int]int{}
	byElement = map[int]int{}
	for rows.Next() {
		var pid, element int
		if err := rows.Scan(&pid, &element); err != nil {
			return nil, nil, err
		}
		byPlayer[pid] = element
		byElement[element] = pid
	}
	return byPlayer, byElement, rows.Err()
}

// LoadPositions returns player_id -> position code for a season.
func LoadPositions(db *sql.DB, season int) (map[int]string, error) {
	rows, err := db.Query(`
		SELECT fsi.player_id, p.element_type
		FROM fpl_season_info fsi
		JOIN fpl_positions p ON p.id = fsi.fpl_positions_id
		WHERE fsi.season = $1`, season)
	if err != nil {
		return nil, fmt.Errorf("loading positions: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var pid int
		var category string
		if err := rows.Scan(&pid, &category); err != nil {
			return nil, err
		}
		if code, ok := positionCode[category]; ok {
			out[pid] = code
		}
	}
	return out, rows.Err()
}

// LoadStartingPrices returns player_id -> starting price in tenths from
// fpl_season_info.starting_price (stored in millions).
func LoadStartingPrices(db *sql.DB, season int) (map[int]int, error) {
	rows, err := db.Query(`
		SELECT player_id, starting_price FROM fpl_season_info
		WHERE season = $1 AND starting_price IS NOT NULL`, season)
	if err != nil {
		return nil, fmt.Errorf("loading starting prices: %w", err)
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var pid int
		var price float64
		if err := rows.Scan(&pid, &price); err != nil {
			return nil, err
		}
		out[pid] = millionsToTenths(price)
	}
	return out, rows.Err()
}

// LoadPriceTable returns element -> current price in tenths from the open
// fpl_player_prices rows, and whether any rows were found.
func LoadPriceTable(db *sql.DB, season int) (map[int]int, bool, error) {
	rows, err := db.Query(`
		SELECT fpl_element, price FROM fpl_player_prices
		WHERE season = $1 AND end_time IS NULL`, season)
	if err != nil {
		return nil, false, fmt.Errorf("loading price table: %w", err)
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var element int
		var price float64
		if err := rows.Scan(&element, &price); err != nil {
			return nil, false, err
		}
		out[element] = millionsToTenths(price)
	}
	return out, len(out) > 0, rows.Err()
}

// LoadFallbackPrices returns element -> price in tenths from the season's
// starting prices, overridden by the last completed gameweek's value where
// available. Used when the price table has no data yet.
func LoadFallbackPrices(db *sql.DB, season int) (map[int]int, error) {
	rows, err := db.Query(`
		SELECT fsi.fpl_season_id, fsi.starting_price
		FROM fpl_season_info fsi
		WHERE fsi.season = $1 AND fsi.fpl_season_id IS NOT NULL
		  AND fsi.starting_price IS NOT NULL`, season)
	if err != nil {
		return nil, fmt.Errorf("loading fallback prices: %w", err)
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var element int
		var price float64
		if err := rows.Scan(&element, &price); err != nil {
			return nil, err
		}
		out[element] = millionsToTenths(price)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Override with the last completed gameweek's value where present.
	rows, err = db.Query(`
		SELECT fsi.fpl_season_id, pg.value
		FROM fpl_players_gameweeks pg
		JOIN fpl_gameweeks g ON g.id = pg.fpl_gameweek_id
		JOIN fpl_season_info fsi ON fsi.player_id = pg.player_id AND fsi.season = g.season
		WHERE g.season = $1
		  AND g.gameweek = (SELECT MAX(g2.gameweek) FROM fpl_gameweeks g2
		                    JOIN fpl_players_gameweeks pg2 ON pg2.fpl_gameweek_id = g2.id
		                    WHERE g2.season = $1)`, season)
	if err != nil {
		return nil, fmt.Errorf("loading fallback gameweek values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var element int
		var price float64
		if err := rows.Scan(&element, &price); err != nil {
			return nil, err
		}
		out[element] = millionsToTenths(price)
	}
	return out, rows.Err()
}

// ResolveCurrentPrices returns element -> current price in tenths,
// preferring the price table and falling back to starting/last-GW values
// with a warning.
func ResolveCurrentPrices(db *sql.DB, season int) (map[int]int, []string, error) {
	prices, found, err := LoadPriceTable(db, season)
	if err != nil {
		return nil, nil, err
	}
	if found {
		return prices, nil, nil
	}

	fallback, err := LoadFallbackPrices(db, season)
	if err != nil {
		return nil, nil, err
	}
	return fallback, []string{
		"no price rows in fpl_player_prices; using starting/last-gameweek prices (run update-fpl-info to populate prices)",
	}, nil
}

// millionsToTenths converts a price stored in millions (e.g. 6.5) to
// integer tenths (65).
func millionsToTenths(m float64) int {
	return int(math.Round(m * 10))
}

// FreeTransferCount reconstructs the free transfers currently available.
// One free transfer is earned per completed gameweek (capped at 5), and
// each transfer consumes one unless it was made in a wildcard or free-hit
// gameweek. Transfers already made for the upcoming gameweek are deducted.
func FreeTransferCount(history []HistoryRow, transfers []Transfer, chips []Chip, upcoming int) int {
	historyTransfers := map[int]int{}
	for _, h := range history {
		historyTransfers[h.Event] = h.EventTransfers
	}
	rowTransfers := map[int]int{}
	for _, t := range transfers {
		rowTransfers[t.Event]++
	}

	used := func(gw int) int {
		if n, ok := historyTransfers[gw]; ok {
			return n
		}
		return rowTransfers[gw]
	}

	wildcardGW := map[int]bool{}
	freeHitGW := map[int]bool{}
	for _, c := range chips {
		switch c.Name {
		case "wildcard":
			wildcardGW[c.Event] = true
		case "freehit":
			freeHitGW[c.Event] = true
		}
	}

	ft := 1 // first free transfer is received after GW1
	for gw := 2; gw < upcoming; gw++ {
		if wildcardGW[gw] {
			// Wildcard resets banked FTs; you receive 1 fresh FT.
			ft = 1
			continue
		}
		u := used(gw)
		if freeHitGW[gw] {
			// Free hit doesn't consume FTs (squad reverts).
			u = 0
		}
		consumed := min(u, ft)
		ft = ft - consumed + 1
		if ft > 5 {
			ft = 5
		}
	}

	// Transfers already made for the upcoming gameweek consume free
	// transfers now; the gameweek itself has not completed yet.
	if wildcardGW[upcoming] {
		ft = 1
	}
	u := used(upcoming)
	if freeHitGW[upcoming] || wildcardGW[upcoming] {
		u = 0
	}
	ft -= min(u, ft)
	if ft < 0 {
		ft = 0
	}
	return ft
}

// BuildPool assembles the transferable player pool from projections,
// current prices and team/position data.
func BuildPool(players map[int]*squad.Player, prices map[int]int, byPlayer map[int]int, teams map[int]projection.PlayerTeamInfo) *Pool {
	pool := &Pool{ByElement: map[int]*PoolPlayer{}, ByPlayer: byPlayer}
	for pid, p := range players {
		element := byPlayer[pid]
		if element == 0 {
			continue // not in the classic game
		}
		team := teams[pid]
		pool.ByElement[element] = &PoolPlayer{
			Element:  element,
			PlayerID: pid,
			Player:   p,
			TeamID:   team.TeamID,
			TeamName: team.TeamName,
			Price:    prices[element],
		}
	}
	return pool
}

// LoadSquad reconstructs the current squad (last synced picks plus later
// transfers), its captain and vice captain, bank and free transfers.
func LoadSquad(db *sql.DB, entry *Entry, pool *Pool, currentPrices map[int]int, startingPrices map[int]int, byPlayer map[int]int, positions map[int]string, teams map[int]projection.PlayerTeamInfo, history []HistoryRow, transfers []Transfer, chips []Chip, upcoming int) (*Squad, error) {
	// Latest picks event.
	var lastEvent int
	err := db.QueryRow(`
		SELECT COALESCE(MAX(event), 0) FROM fpl_classic_picks
		WHERE classic_entry_id = $1`, entry.ID).Scan(&lastEvent)
	if err != nil {
		return nil, fmt.Errorf("loading last picks event: %w", err)
	}
	if lastEvent == 0 {
		return nil, fmt.Errorf("no lineup picks found for entry %s (%d); run update-fpl-classic", entry.Name, entry.FPLID)
	}

	s := &Squad{Members: map[int]*Member{}}

	// Seed the squad from the latest picks.
	rows, err := db.Query(`
		SELECT fpl_element, player_id, is_captain, is_vice_captain, active_chip
		FROM fpl_classic_picks
		WHERE classic_entry_id = $1 AND event = $2`, entry.ID, lastEvent)
	if err != nil {
		return nil, fmt.Errorf("loading lineup picks: %w", err)
	}
	var activeChip string
	for rows.Next() {
		var element int
		var playerID sql.NullInt64
		var isCaptain, isVice bool
		var chip sql.NullString
		if err := rows.Scan(&element, &playerID, &isCaptain, &isVice, &chip); err != nil {
			rows.Close()
			return nil, err
		}
		if chip.Valid {
			activeChip = chip.String
		}
		pid := int(playerID.Int64)
		m := buildMember(element, pid, pool, currentPrices, startingPrices, positions, teams)
		s.Members[element] = m
		if isCaptain {
			s.Captain = element
		}
		if isVice {
			s.ViceCaptain = element
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Set purchase prices from transfer history: the most recent
	// element_in_cost for each element still in the squad overrides the
	// starting_price fallback (a player bought mid-season has a real
	// purchase price recorded in fpl_classic_transfers).
	for _, t := range transfers {
		if t.ElementInCost > 0 {
			if m, ok := s.Members[t.ElementIn]; ok {
				m.PurchasePrice = t.ElementInCost
			}
		}
	}

	// Apply transfers made after the last picks event, in time order.
	for _, t := range transfers {
		if t.Event <= lastEvent {
			continue
		}
		delete(s.Members, t.ElementOut)
		m := buildMember(t.ElementIn, t.PlayerInID, pool, currentPrices, startingPrices, positions, teams)
		if t.ElementInCost > 0 {
			m.PurchasePrice = t.ElementInCost
		}
		s.Members[t.ElementIn] = m
	}

	// A captain or vice captain who left the squad is unknown now.
	if _, ok := s.Members[s.Captain]; !ok {
		s.Captain = 0
	}
	if _, ok := s.Members[s.ViceCaptain]; !ok {
		s.ViceCaptain = 0
	}

	// Bank: latest completed gameweek plus any transfers already made for
	// the upcoming gameweek.
	if len(history) > 0 {
		s.Bank = history[len(history)-1].Bank
	}
	for _, t := range transfers {
		if t.Event == upcoming {
			s.Bank += t.ElementOutCost - t.ElementInCost
		}
	}

	s.FreeTransfers = FreeTransferCount(history, transfers, chips, upcoming)
	s.Warnings = squadWarnings(s.Members, activeChip)
	if err := fillNames(db, s.Members); err != nil {
		return nil, err
	}
	return s, nil
}

// buildMember assembles a squad member, filling in the projection, position
// and prices where the data allows.
func buildMember(element, pid int, pool *Pool, currentPrices map[int]int, startingPrices map[int]int, positions map[int]string, teams map[int]projection.PlayerTeamInfo) *Member {
	m := &Member{
		Element:       element,
		PlayerID:      pid,
		CurrentPrice:  currentPrices[element],
		PurchasePrice: startingPrices[pid],
	}
	if pid != 0 {
		m.Position = positions[pid]
		m.TeamID = teams[pid].TeamID
	}
	if pp, ok := pool.ByElement[element]; ok && pp.Player != nil {
		m.Player = pp.Player
		m.Position = pp.Player.Position
		m.TeamID = pp.TeamID
		m.Name = pp.Player.Name
		// The pool price is the same source as currentPrices, but ensure
		// they agree.
		if m.CurrentPrice == 0 {
			m.CurrentPrice = pp.Price
		}
	} else if m.Position != "" && pid != 0 {
		// Unprojected but positioned: a synthetic 0-point player keeps the
		// squad shape valid so the XI optimiser can run.
		m.Player = &squad.Player{ID: pid, Position: m.Position}
	}
	return m
}

// LoadPlayerNames returns player_id -> display name from the players table.
func LoadPlayerNames(db *sql.DB, playerIDs []int) (map[int]string, error) {
	if len(playerIDs) == 0 {
		return map[int]string{}, nil
	}
	rows, err := db.Query(`
		SELECT id, first_name || ' ' || last_name
		FROM players WHERE id = ANY($1)`, pq.Array(playerIDs))
	if err != nil {
		return nil, fmt.Errorf("loading player names: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// fillNames fills in display names for squad members that lack one
// (unprojected players).
func fillNames(db *sql.DB, members map[int]*Member) error {
	var ids []int
	for _, m := range members {
		if m.Name == "" && m.PlayerID != 0 {
			ids = append(ids, m.PlayerID)
		}
	}
	names, err := LoadPlayerNames(db, ids)
	if err != nil {
		return err
	}
	for _, m := range members {
		if n, ok := names[m.PlayerID]; ok && m.Name == "" {
			m.Name = n
		}
	}
	return nil
}

// squadWarnings surfaces data problems that affect the recommendation:
// unmatched or unprojected squad members and an active free hit.
func squadWarnings(members map[int]*Member, activeChip string) []string {
	var w []string
	for _, m := range members {
		switch {
		case m.PlayerID == 0:
			w = append(w, fmt.Sprintf("element %d is unmatched to a mandyville player and is treated as 0 points", m.Element))
		case m.Player == nil:
			w = append(w, fmt.Sprintf("player %d has no projection and is treated as 0 points", m.PlayerID))
		}
	}
	if activeChip == "freehit" {
		w = append(w, "a free hit was active in the last synced gameweek; the reconstructed squad may not be your real squad")
	}
	return w
}
