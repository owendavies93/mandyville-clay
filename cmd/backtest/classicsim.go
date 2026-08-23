package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mandyville/mandyville-draft/classic"
	"github.com/mandyville/mandyville-draft/projection"
	"github.com/mandyville/mandyville-draft/squad"
)

// simProjVersion identifies the projection cache format; bump it when the
// engine changes so stale caches are regenerated.
const simProjVersion = "v1"

// simSquad is a strategy's live state across the simulated season.
type simSquad struct {
	squad     classic.Squad
	total     int // actual points scored so far (hits deducted)
	transfers int
	hits      int
	gwPoints  map[int]int // gameweek -> net actual points
}

// simStrategy is one transfer policy. decide returns this week's transfers
// for the evolving squad.
type simStrategy struct {
	name   string
	decide func(gw int, planner *classic.Planner, sq *simSquad) []classic.Move
}

func runClassicSim(db *sql.DB, season, beam, horizon int, minGain float64, refresh bool) {
	actual, err := projection.LoadActualGWPoints(db, season)
	if err != nil {
		fatalf("loading actual points: %v", err)
	}
	minutes, err := loadMinutes(db, season)
	if err != nil {
		fatalf("loading minutes: %v", err)
	}
	deadlines, err := projection.LoadGameweekDeadlines(db, season)
	if err != nil {
		fatalf("loading deadlines: %v", err)
	}
	maxGW := 38
	for gw := range deadlines {
		if gw > maxGW {
			maxGW = gw
		}
	}
	if _, ok := deadlines[1]; !ok {
		fatalf("no gameweek 1 deadline for season %d", season)
	}

	// Opening prices: prefer fpl_season_info.starting_price, fall back to
	// GW1 values (the same fallback the squad selector uses).
	priceRows, err := projection.LoadPlayerPrices(db, season)
	if err != nil {
		fatalf("loading player prices: %v", err)
	}
	startingPrices := map[int]int{}
	for id, p := range priceRows {
		startingPrices[id] = int(math.Round(p.Price * 10))
	}

	// Point-in-time projections, cached to disk.
	cacheDir := filepath.Join("out", "sim-projections", fmt.Sprintf("%d", season), simProjVersion)
	projections := make([]*projection.ProjectionOutput, maxGW+1)
	for gw := 1; gw <= maxGW; gw++ {
		out, err := loadOrComputeProjection(db, cacheDir, season, gw, refresh)
		if err != nil {
			fatalf("projection at gameweek %d: %v", gw, err)
		}
		projections[gw] = out
	}

	// Opening squad from gameweek-1 projections and starting prices.
	players1 := toSquadPlayers(projections[1])
	opening, openingBank := openingSquad(players1, startingPrices, teamIDsOf(projections[1]))

	fmt.Printf("Classic season simulator: season %d, gameweeks 1-%d, opening squad £%.1fm\n",
		season, maxGW, float64(openingValue(opening, startingPrices))/10)

	strategies := []simStrategy{
		{name: "never-transfer", decide: func(gw int, p *classic.Planner, sq *simSquad) []classic.Move {
			return nil
		}},
		{name: "greedy-1ft", decide: func(gw int, p *classic.Planner, sq *simSquad) []classic.Move {
			cands := p.ThisWeekSingles(&sq.squad)
			if len(cands) > 0 && cands[0].Gain > minGain {
				return []classic.Move{cands[0]}
			}
			return nil
		}},
		{name: "planner", decide: func(gw int, p *classic.Planner, sq *simSquad) []classic.Move {
			outcome, err := p.Plan(&sq.squad)
			if err != nil || outcome.Recommended == nil {
				return nil
			}
			roll := 0.0
			if outcome.RollThisWeek != nil {
				roll = outcome.RollThisWeek.Total
			}
			if outcome.Recommended.Total-roll >= minGain {
				return outcome.Recommended.Immediate
			}
			return nil
		}},
	}

	var results []simResult

	for _, st := range strategies {
		sq := simSquad{
			squad:    classic.Squad{Members: cloneMembers(opening), Bank: openingBank, FreeTransfers: 1},
			gwPoints: map[int]int{},
		}
		for gw := 1; gw <= maxGW; gw++ {
			prices := loadGWValues(db, season, gw)
			if len(prices) == 0 && gw > 1 {
				prices = loadGWValues(db, season, gw-1)
			}

			// Refresh current prices on owned players so sales use the
			// gameweek's value.
			for _, m := range sq.squad.Members {
				if p, ok := prices[m.PlayerID]; ok {
					m.CurrentPrice = p
				}
			}

			playersGW := toSquadPlayers(projections[gw])
			pool := buildSimPool(playersGW, prices, teamIDsOf(projections[gw]))

			h := horizon
			if gw+h-1 > maxGW {
				h = maxGW - gw + 1
			}
			if h < 1 {
				h = 1
			}
			planner := classic.NewPlanner(pool, gw, h, beam, 2, 30)

			moves := st.decide(gw, planner, &sq)
			applySimMoves(&sq, moves)

			pts := scoreGameweek(&sq.squad, gw, actual, minutes, playersGW)
			sq.total += pts
			sq.gwPoints[gw] = pts

			// Earn one free transfer for completing the gameweek.
			sq.squad.FreeTransfers++
			if sq.squad.FreeTransfers > 5 {
				sq.squad.FreeTransfers = 5
			}
		}

		results = append(results, simResult{
			name: st.name, gwPoints: sq.gwPoints, total: sq.total,
			transfers: sq.transfers, hits: sq.hits,
			finalVal: squadTotalValue(&sq.squad),
		})
	}

	printSimResults(results, maxGW)
	writeSimJSON(season, results, maxGW)
}

func applySimMoves(sq *simSquad, moves []classic.Move) {
	freeUsed := len(moves)
	if freeUsed > sq.squad.FreeTransfers {
		freeUsed = sq.squad.FreeTransfers
	}
	hits := len(moves) - freeUsed

	for _, m := range moves {
		delete(sq.squad.Members, m.OutElement)
		sq.squad.Members[m.InElement] = &classic.Member{
			Element:       m.InElement,
			PlayerID:      m.In.PlayerID,
			Player:        m.In.Player,
			Position:      m.Position,
			Name:          m.In.Player.Name,
			TeamID:        m.In.TeamID,
			PurchasePrice: m.In.Price,
			CurrentPrice:  m.In.Price,
		}
		sq.squad.Bank += m.Proceeds - m.Cost
	}
	sq.squad.FreeTransfers -= freeUsed
	sq.transfers += len(moves)
	sq.hits += hits
	sq.total -= 4 * hits
}

func scoreGameweek(sq *classic.Squad, gw int, actual, minutes map[int]map[int]int, players map[int]*squad.Player) int {
	// The XI, captain and bench are chosen with the current gameweek's
	// projections (the manager re-evaluates the whole squad each week).
	roster := map[int]*squad.Player{}
	for _, m := range sq.Members {
		if p, ok := players[m.PlayerID]; ok && p != nil {
			roster[m.PlayerID] = p
		}
	}
	xi, captain, _ := squad.BestXIWithCaptain(roster, gw)
	bench := squad.BenchOrder(roster, xi, gw)
	vice := viceCaptain(roster, xi, captain, gw)

	posByID := map[int]string{}
	for id, p := range roster {
		posByID[id] = p.Position
	}

	final := applyAutosubs(xi, bench, minutes, posByID, gw)

	total := 0
	for _, id := range final {
		total += actual[id][gw]
	}
	if minutes[captain][gw] == 0 {
		total += actual[vice][gw]
	} else {
		total += actual[captain][gw]
	}
	return total
}

// viceCaptain returns the second-highest projected player in the XI.
func viceCaptain(roster map[int]*squad.Player, xi []int, captain, gw int) int {
	vice := 0
	best := math.Inf(-1)
	for _, id := range xi {
		if id == captain {
			continue
		}
		if p := roster[id].PointsIn(gw); p > best {
			best = p
			vice = id
		}
	}
	if vice == 0 {
		vice = captain
	}
	return vice
}

// applyAutosubs replaces non-playing starters with the first eligible bench
// player who keeps the formation legal, per FPL rules.
func applyAutosubs(xi, bench []int, minutes map[int]map[int]int, posByID map[int]string, gw int) []int {
	out := append([]int(nil), xi...)
	used := make([]bool, len(bench))

	for i, id := range xi {
		if minutes[id][gw] > 0 {
			continue
		}
		for j, b := range bench {
			if used[j] || minutes[b][gw] == 0 {
				continue
			}
			if posByID[id] == squad.PosGK && posByID[b] != squad.PosGK {
				continue
			}
			if posByID[b] == squad.PosGK && posByID[id] != squad.PosGK {
				continue
			}
			if !formationLegal(out, i, b, posByID) {
				continue
			}
			out[i] = b
			used[j] = true
			break
		}
	}
	return out
}

func formationLegal(out []int, swapIdx, in int, posByID map[int]string) bool {
	counts := map[string]int{}
	for i, id := range out {
		if i == swapIdx {
			counts[posByID[in]]++
		} else {
			counts[posByID[id]]++
		}
	}
	return counts[squad.PosDEF] >= 3 && counts[squad.PosMID] >= 2 && counts[squad.PosFWD] >= 1
}

// --- opening squad selection ---

type simCand struct {
	id, price, team int
	pos             string
	pts             float64
}

func openingSquad(players map[int]*squad.Player, prices map[int]int, teams map[int]int) (map[int]*classic.Member, int) {
	const budget = 1000

	byPos := map[string][]simCand{}
	for id, p := range players {
		price, ok := prices[id]
		if !ok || price <= 0 {
			continue
		}
		pts := 0.0
		for gw := 1; gw <= 6; gw++ {
			pts += p.PointsIn(gw)
		}
		byPos[p.Position] = append(byPos[p.Position], simCand{id: id, price: price, team: teams[id], pos: p.Position, pts: pts})
	}
	for pos := range byPos {
		sort.Slice(byPos[pos], func(i, j int) bool {
			vi := byPos[pos][i].pts / float64(byPos[pos][i].price)
			vj := byPos[pos][j].pts / float64(byPos[pos][j].price)
			return vi > vj
		})
	}

	want := []struct {
		pos   string
		count int
	}{
		{squad.PosGK, 2}, {squad.PosDEF, 5}, {squad.PosMID, 5}, {squad.PosFWD, 3},
	}
	var slots []string
	for _, w := range want {
		for i := 0; i < w.count; i++ {
			slots = append(slots, w.pos)
		}
	}
	// Fill forwards/midfielders before defenders and keepers so the budget
	// is spent on the high-value slots first.
	sort.SliceStable(slots, func(i, j int) bool {
		return slotOrder(slots[i]) < slotOrder(slots[j])
	})

	members := map[int]*classic.Member{}
	used := map[int]bool{}
	club := map[int]int{}
	bank := budget

	for idx, pos := range slots {
		// Reserve the cheapest available player for each remaining slot.
		reserve := 0
		for _, rp := range slots[idx+1:] {
			reserve += cheapestFor(byPos[rp], used)
		}
		maxPrice := bank - reserve
		if maxPrice < 0 {
			maxPrice = 0
		}

		chosen := -1
		for _, c := range byPos[pos] {
			if used[c.id] || club[c.team] >= 3 || c.price > maxPrice {
				continue
			}
			chosen = c.id
			break
		}
		if chosen == -1 {
			fatalf("opening squad: no affordable %s player", pos)
		}
		used[chosen] = true
		club[teams[chosen]]++
		bank -= prices[chosen]
		members[chosen] = &classic.Member{
			Element:       chosen,
			PlayerID:      chosen,
			Player:        players[chosen],
			Position:      players[chosen].Position,
			Name:          players[chosen].Name,
			TeamID:        teams[chosen],
			PurchasePrice: prices[chosen],
			CurrentPrice:  prices[chosen],
		}
	}
	return members, bank
}

func slotOrder(pos string) int {
	switch pos {
	case squad.PosFWD:
		return 0
	case squad.PosMID:
		return 1
	case squad.PosDEF:
		return 2
	default:
		return 3
	}
}

func cheapestFor(cands []simCand, used map[int]bool) int {
	best := math.MaxInt
	for _, c := range cands {
		if used[c.id] {
			continue
		}
		if c.price < best {
			best = c.price
		}
	}
	return best
}

// --- reporting ---

type simResult struct {
	name      string
	gwPoints  map[int]int
	total     int
	transfers int
	hits      int
	finalVal  int
}

func printSimResults(results []simResult, maxGW int) {
	base := 0
	for _, r := range results {
		if r.name == "never-transfer" {
			base = r.total
		}
	}

	fmt.Printf("\n%-14s %10s %10s %6s %5s %10s\n", "strategy", "points", "delta", "tfrs", "hits", "squad £")
	for _, r := range results {
		fmt.Printf("%-14s %10d %+10d %6d %5d %10.1f\n",
			r.name, r.total, r.total-base, r.transfers, r.hits, float64(r.finalVal)/10)
	}

	fmt.Println("\nPoints per gameweek:")
	header := "GW       "
	for _, r := range results {
		header += fmt.Sprintf("%-16s", r.name)
	}
	fmt.Println(header)
	for gw := 1; gw <= maxGW; gw++ {
		line := fmt.Sprintf("GW%-2d     ", gw)
		for _, r := range results {
			line += fmt.Sprintf("%-16d", r.gwPoints[gw])
		}
		fmt.Println(line)
	}
}

func writeSimJSON(season int, results []simResult, maxGW int) {
	base := 0
	for _, r := range results {
		if r.name == "never-transfer" {
			base = r.total
		}
	}

	type stratOut struct {
		Name      string      `json:"name"`
		Total     int         `json:"total_points"`
		Delta     int         `json:"delta_vs_never_transfer"`
		Transfers int         `json:"transfers"`
		Hits      int         `json:"hits"`
		FinalVal  float64     `json:"final_squad_value"`
		ByGW      map[int]int `json:"points_by_gameweek"`
	}
	out := struct {
		Season     int        `json:"season"`
		Gameweeks  int        `json:"gameweeks"`
		Strategies []stratOut `json:"strategies"`
	}{Season: season, Gameweeks: maxGW}

	for _, r := range results {
		out.Strategies = append(out.Strategies, stratOut{
			Name: r.name, Total: r.total, Delta: r.total - base,
			Transfers: r.transfers, Hits: r.hits,
			FinalVal: float64(r.finalVal) / 10, ByGW: r.gwPoints,
		})
	}

	path := filepath.Join("out", fmt.Sprintf("classic-sim-%d.json", season))
	f, err := os.Create(path)
	if err != nil {
		fatalf("writing sim JSON: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fatalf("encoding sim JSON: %v", err)
	}
	fmt.Printf("\nWrote %s\n", path)
}

// --- loaders and helpers ---

func loadOrComputeProjection(db *sql.DB, cacheDir string, season, gw int, refresh bool) (*projection.ProjectionOutput, error) {
	path := filepath.Join(cacheDir, fmt.Sprintf("gw%d.json", gw))
	if !refresh {
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			var out projection.ProjectionOutput
			if err := json.NewDecoder(f).Decode(&out); err == nil {
				return &out, nil
			}
		}
	}

	engine := projection.NewEngine(db, season, 1)
	engine.Backtest = true
	engine.Rules = projection.ClassicRules
	out, err := engine.RunInSeason(gw)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(out); err != nil {
		return nil, err
	}
	return out, nil
}

func toSquadPlayers(out *projection.ProjectionOutput) map[int]*squad.Player {
	players := map[int]*squad.Player{}
	for i := range out.Players {
		p := &out.Players[i]
		gw := map[int]float64{}
		for _, fx := range p.Gameweeks {
			gw[fx.Gameweek] += fx.ProjectedPoints
		}
		players[p.PlayerID] = &squad.Player{
			ID: p.PlayerID, Name: strings.TrimSpace(p.FirstName + " " + p.LastName),
			Position: p.Position, GWPoints: gw, Consistency: p.Consistency,
		}
	}
	return players
}

func teamIDsOf(out *projection.ProjectionOutput) map[int]int {
	teams := map[int]int{}
	for _, p := range out.Players {
		teams[p.PlayerID] = p.TeamID
	}
	return teams
}

func buildSimPool(players map[int]*squad.Player, prices map[int]int, teams map[int]int) *classic.Pool {
	pool := &classic.Pool{ByElement: map[int]*classic.PoolPlayer{}, ByPlayer: map[int]int{}}
	for id, p := range players {
		pool.ByElement[id] = &classic.PoolPlayer{
			Element: id, PlayerID: id, Player: p, TeamID: teams[id], Price: prices[id],
		}
		pool.ByPlayer[id] = id
	}
	return pool
}

func loadGWValues(db *sql.DB, season, gw int) map[int]int {
	rows, err := db.Query(`
		SELECT pg.player_id, pg.value
		FROM fpl_players_gameweeks pg
		JOIN fpl_gameweeks g ON g.id = pg.fpl_gameweek_id
		WHERE g.season = $1 AND g.gameweek = $2`, season, gw)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var id int
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			continue
		}
		out[id] = int(math.Round(v * 10))
	}
	return out
}

func loadMinutes(db *sql.DB, season int) (map[int]map[int]int, error) {
	rows, err := db.Query(`
		SELECT pf.player_id, g.gameweek, SUM(pf.minutes)
		FROM players_fixtures pf
		JOIN fixtures_fpl_gameweeks ffg ON ffg.fixture_id = pf.fixture_id
		JOIN fpl_gameweeks g ON g.id = ffg.gameweek_id
		WHERE g.season = $1
		GROUP BY 1, 2`, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]map[int]int{}
	for rows.Next() {
		var id, gw, mins int
		if err := rows.Scan(&id, &gw, &mins); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[int]int{}
		}
		out[id][gw] += mins
	}
	return out, rows.Err()
}

func cloneMembers(m map[int]*classic.Member) map[int]*classic.Member {
	out := make(map[int]*classic.Member, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func openingValue(members map[int]*classic.Member, prices map[int]int) int {
	total := 0
	for id := range members {
		total += prices[id]
	}
	return total
}

func squadTotalValue(s *classic.Squad) int {
	total := s.Bank
	for _, m := range s.Members {
		total += m.CurrentPrice
	}
	return total
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
