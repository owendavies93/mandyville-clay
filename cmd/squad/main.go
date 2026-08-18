package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mandyville/mandyville-draft/projection"
)

// Position slot indices (also squad limit order).
const (
	slotGK  = 0
	slotDEF = 1
	slotMID = 2
	slotFWD = 3
)

var slotNames = [4]string{"GK", "DEF", "MID", "FWD"}

// squadLimits is the required squad composition: 2 GK, 5 DEF, 5 MID, 3 FWD.
var squadLimits = [4]int{2, 5, 5, 3}

// formation is an outfield shape for the starting XI (DEF-MID-FWD).
type formation struct {
	def int
	mid int
	fwd int
}

// All valid formations: >=3 DEF, >=2 MID, >=1 FWD, MID<=5, FWD<=3.
var formations = []formation{
	{3, 4, 3}, {3, 5, 2}, {4, 3, 3}, {4, 4, 2},
	{4, 5, 1}, {5, 2, 3}, {5, 3, 2}, {5, 4, 1},
}

// cPlayer is a projection plus price and fixture-adjusted points.
type cPlayer struct {
	proj      projection.PlayerProjection
	price     float64
	teamID    int       // team from the projection (players_teams table)
	teamName  string    // display name for teamID
	adjPoints float64   // window-adjusted points (GW 1..N)
	gw1Points float64   // GW1-only points (for captain selection)
	gwPoints  []float64 // projected points per gameweek, index gw-1
}

func (p cPlayer) slot() int {
	switch p.proj.Position {
	case "GK":
		return slotGK
	case "DEF":
		return slotDEF
	case "MID":
		return slotMID
	case "FWD":
		return slotFWD
	}
	return -1
}

func main() {
	input := flag.String("input", "projections.json", "projection JSON file")
	budget := flag.Float64("budget", 100.0, "total budget in millions")
	season := flag.Int("season", 2026, "season for prices and fixtures")
	gameweeks := flag.Int("gameweeks", 6, "number of opening gameweeks to optimise for")
	jsonOut := flag.String("json", "", "write the selected squad to this file as JSON")
	configFile := flag.String("config", "", "path to mandyville config.yaml")
	dbHost := flag.String("db-host", "", "database host")
	dbPort := flag.Int("db-port", 0, "database port")
	dbUser := flag.String("db-user", "", "database user")
	dbPass := flag.String("db-pass", "", "database password")
	dbName := flag.String("db-name", "", "database name")
	flag.Parse()

	if *gameweeks < 1 || *gameweeks > 38 {
		fmt.Fprintf(os.Stderr, "-gameweeks must be between 1 and 38\n")
		os.Exit(1)
	}

	// Load projections.
	f, err := os.Open(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open %s: %v\n", *input, err)
		os.Exit(1)
	}
	defer f.Close()

	var data projection.ProjectionOutput
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse projections: %v\n", err)
		os.Exit(1)
	}

	cfg := projection.DBConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "password",
		DBName:   "mandyville",
	}
	if *configFile != "" {
		var err error
		cfg, err = projection.LoadDBConfigFromFile(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
			os.Exit(1)
		}
	}
	if *dbHost != "" {
		cfg.Host = *dbHost
	}
	if *dbPort != 0 {
		cfg.Port = *dbPort
	}
	if *dbUser != "" {
		cfg.User = *dbUser
	}
	if *dbPass != "" {
		cfg.Password = *dbPass
	}
	if *dbName != "" {
		cfg.DBName = *dbName
	}
	db, err := projection.OpenDB(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		os.Exit(1)
	}

	// Load prices.
	prices, err := projection.LoadPlayerPrices(db, *season)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load prices: %v\n", err)
		os.Exit(1)
	}
	if len(prices) == 0 {
		fmt.Fprintf(os.Stderr, "no prices found for season %d\n", *season)
		os.Exit(1)
	}

	// Build the playable list: players with a known price. Per-gameweek
	// points come straight from the engine's fixture-level projections, so
	// blanks, doubles and fixture difficulty are already accounted for.
	var players []cPlayer
	for _, p := range data.Players {
		pp, ok := prices[p.PlayerID]
		if !ok {
			continue
		}

		gwPoints := make([]float64, *gameweeks)
		var adjPoints float64
		for _, fx := range p.Gameweeks {
			if fx.Gameweek >= 1 && fx.Gameweek <= *gameweeks {
				gwPoints[fx.Gameweek-1] += fx.ProjectedPoints
			}
		}
		for _, pts := range gwPoints {
			adjPoints += pts
		}

		players = append(players, cPlayer{
			proj:      p,
			price:     pp.Price,
			teamID:    p.TeamID,
			teamName:  p.TeamName,
			adjPoints: adjPoints,
			gw1Points: gwPoints[0],
			gwPoints:  gwPoints,
		})
	}

	if len(players) == 0 {
		fmt.Fprintln(os.Stderr, "no players with prices found")
		os.Exit(1)
	}

	squad := optimise(players, *budget)

	printSquad(players, squad, *budget, *gameweeks)

	if *jsonOut != "" {
		if err := writeSquadJSON(*jsonOut, players, squad, *budget, *season, *gameweeks); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write JSON: %v\n", err)
			os.Exit(1)
		}
	}
}

// squad is a selected 15-man team.
type squad struct {
	xi      []int // indices into players
	bench   []int
	captain int // index into players (also in xi)
}

func (s squad) score(players []cPlayer) float64 {
	total := 0.0
	for _, i := range s.xi {
		total += players[i].adjPoints
	}
	// Captain bonus is the GW1 doubling only: the captain is re-picked
	// every gameweek, so only the opening week matters here.
	total += players[s.captain].gw1Points
	return total
}

func (s squad) cost(players []cPlayer) float64 {
	total := 0.0
	for _, i := range s.xi {
		total += players[i].price
	}
	for _, i := range s.bench {
		total += players[i].price
	}
	return total
}

func (s squad) clubCount(players []cPlayer) map[int]int {
	m := map[int]int{}
	for _, i := range s.xi {
		m[players[i].teamID]++
	}
	for _, i := range s.bench {
		m[players[i].teamID]++
	}
	return m
}

// optimise finds the best squad across all formations via greedy +
// swap improvement.
func optimise(players []cPlayer, budget float64) squad {
	var best squad
	bestScore := -1.0

	for _, form := range formations {
		xi, bench, ok := greedySquad(players, budget, form)
		if !ok {
			continue
		}
		s := squad{xi: xi, bench: bench}
		s.captain = pickCaptain(players, s.xi)
		s = improve(players, s, budget, form)

		if sc := s.score(players); sc > bestScore {
			best = s
			bestScore = sc
		}
	}
	return best
}

// greedySquad builds a starting XI (respecting budget and club limits)
// then fills the bench with the cheapest valid players. Each pick reserves
// enough budget to afford the cheapest remaining slots, so the greedy
// always yields a feasible squad when one exists.
func greedySquad(players []cPlayer, budget float64, form formation) ([]int, []int, bool) {
	n := len(players)
	used := make([]bool, n)
	clubCount := map[int]int{}

	// Ordered picks: XI first (high-value positions before defenders),
	// then bench filled with cheapest.
	type pick struct {
		slot    int
		isBench bool
	}
	xiOrder := []int{slotGK, slotFWD, slotMID, slotDEF}
	benchOrder := []int{slotGK, slotDEF, slotMID, slotFWD}

	xiCounts := map[int]int{slotGK: 1, slotFWD: form.fwd, slotMID: form.mid, slotDEF: form.def}
	benchCounts := map[int]int{
		slotGK:  squadLimits[slotGK] - 1,
		slotDEF: squadLimits[slotDEF] - form.def,
		slotMID: squadLimits[slotMID] - form.mid,
		slotFWD: squadLimits[slotFWD] - form.fwd,
	}

	var picks []pick
	for _, s := range xiOrder {
		for k := 0; k < xiCounts[s]; k++ {
			picks = append(picks, pick{s, false})
		}
	}
	for _, s := range benchOrder {
		for k := 0; k < benchCounts[s]; k++ {
			picks = append(picks, pick{s, true})
		}
	}

	budgetLeft := budget
	var xi, bench []int
	for pi, pk := range picks {
		// Reserve the cost of the cheapest available player for each
		// remaining slot so we never paint ourselves into a corner.
		reserve := 0.0
		for _, rp := range picks[pi+1:] {
			reserve += cheapestPrice(players, used, clubCount, rp.slot)
		}
		maxPrice := budgetLeft - reserve
		if maxPrice < 0 {
			maxPrice = 0
		}

		var idx int
		if pk.isBench {
			idx = cheapestAvailable(players, used, clubCount, pk.slot, maxPrice)
		} else {
			idx = bestAvailable(players, used, clubCount, pk.slot, maxPrice)
		}
		if idx < 0 {
			return nil, nil, false
		}
		used[idx] = true
		clubCount[players[idx].teamID]++
		budgetLeft -= players[idx].price
		if pk.isBench {
			bench = append(bench, idx)
		} else {
			xi = append(xi, idx)
		}
	}

	return xi, bench, true
}

// cheapestPrice returns the lowest price of an available player of the
// given slot (ignoring club limit, for budget-reservation purposes).
func cheapestPrice(players []cPlayer, used []bool, clubCount map[int]int, slot int) float64 {
	best := 1e9
	for i := range players {
		if used[i] || players[i].slot() != slot {
			continue
		}
		if players[i].price < best {
			best = players[i].price
		}
	}
	return best
}

// bestAvailable returns the highest-points player of the given slot that
// is unused, respects the 3-per-club limit, and fits the budget.
func bestAvailable(players []cPlayer, used []bool, clubCount map[int]int, slot int, budgetLeft float64) int {
	best := -1
	bestPoints := -1.0
	for i := range players {
		if used[i] || players[i].slot() != slot {
			continue
		}
		if clubCount[players[i].teamID] >= 3 {
			continue
		}
		if players[i].price > budgetLeft {
			continue
		}
		if players[i].adjPoints > bestPoints {
			bestPoints = players[i].adjPoints
			best = i
		}
	}
	return best
}

// cheapestAvailable returns the cheapest player of the given slot that is
// unused and respects the 3-per-club limit.
func cheapestAvailable(players []cPlayer, used []bool, clubCount map[int]int, slot int, budgetLeft float64) int {
	best := -1
	bestPrice := 1e9
	for i := range players {
		if used[i] || players[i].slot() != slot {
			continue
		}
		if clubCount[players[i].teamID] >= 3 {
			continue
		}
		if players[i].price > budgetLeft {
			continue
		}
		if players[i].price < bestPrice {
			bestPrice = players[i].price
			best = i
		}
	}
	return best
}

// pickCaptain returns the index of the XI player with the best GW1
// projection, since the captain is chosen for the opening gameweek only.
func pickCaptain(players []cPlayer, xi []int) int {
	best := xi[0]
	for _, i := range xi[1:] {
		if players[i].gw1Points > players[best].gw1Points {
			best = i
		}
	}
	return best
}

// improve runs local search: minimise bench cost, then upgrade XI players,
// repeating until no further improvement.
func improve(players []cPlayer, s squad, budget float64, form formation) squad {
	best := s
	for {
		changed := false

		// 1. Minimise bench cost: replace bench players with cheaper
		//    same-position options that keep constraints valid.
		squadSet := map[int]bool{}
		for _, i := range best.xi {
			squadSet[i] = true
		}
		for _, i := range best.bench {
			squadSet[i] = true
		}

		for bi, bidx := range best.bench {
			slot := players[bidx].slot()
			for j := range players {
				if squadSet[j] || players[j].slot() != slot {
					continue
				}
				if players[j].price >= players[bidx].price {
					continue
				}
				// Tentatively swap and check club limit + budget.
				probe := append([]int{}, best.bench...)
				probe[bi] = j
				cand := squad{xi: best.xi, bench: probe, captain: best.captain}
				if clubOK(cand, players) && cand.cost(players) <= budget {
					delete(squadSet, bidx)
					squadSet[j] = true
					best.bench = probe
					changed = true
					break
				}
			}
		}

		// 2. Upgrade XI: replace an XI player with a higher-points
		//    same-position player that keeps constraints valid.
		squadSet = map[int]bool{}
		for _, i := range best.xi {
			squadSet[i] = true
		}
		for _, i := range best.bench {
			squadSet[i] = true
		}

		for xi, xidx := range best.xi {
			slot := players[xidx].slot()
			for j := range players {
				if squadSet[j] || players[j].slot() != slot {
					continue
				}
				if players[j].adjPoints <= players[xidx].adjPoints {
					continue
				}
				probe := append([]int{}, best.xi...)
				probe[xi] = j
				cand := squad{xi: probe, bench: best.bench, captain: pickCaptain(players, probe)}
				if clubOK(cand, players) && cand.cost(players) <= budget {
					if cand.score(players) > best.score(players) {
						delete(squadSet, xidx)
						squadSet[j] = true
						best = cand
						changed = true
						break
					}
				}
			}
		}

		if !changed {
			break
		}
	}
	return best
}

func clubOK(s squad, players []cPlayer) bool {
	for _, c := range s.clubCount(players) {
		if c > 3 {
			return false
		}
	}
	return true
}

// squadJSONPlayer is one selected player in the JSON output.
type squadJSONPlayer struct {
	PlayerID  int       `json:"player_id"`
	Name      string    `json:"name"`
	Position  string    `json:"position"`
	TeamID    int       `json:"team_id"`
	TeamName  string    `json:"team"`
	Price     float64   `json:"price"`
	Projected float64   `json:"projected_window_points"`
	GW1       float64   `json:"projected_gw1_points"`
	ByGW      []float64 `json:"projected_by_gameweek"`
	Captain   bool      `json:"captain"`
}

// squadJSON is the machine-readable form of a selected squad.
type squadJSON struct {
	Season          int               `json:"season"`
	Gameweeks       int               `json:"gameweeks"`
	Budget          float64           `json:"budget"`
	Cost            float64           `json:"cost"`
	Formation       string            `json:"formation"`
	ProjectedPoints float64           `json:"projected_points"`
	CaptainID       int               `json:"captain_player_id"`
	XI              []squadJSONPlayer `json:"xi"`
	Bench           []squadJSONPlayer `json:"bench"`
}

func writeSquadJSON(path string, players []cPlayer, s squad, budget float64, season, gameweeks int) error {
	toJSON := func(i int) squadJSONPlayer {
		p := players[i]
		return squadJSONPlayer{
			PlayerID:  p.proj.PlayerID,
			Name:      fmt.Sprintf("%s %s", p.proj.FirstName, p.proj.LastName),
			Position:  p.proj.Position,
			TeamID:    p.teamID,
			TeamName:  p.teamName,
			Price:     p.price,
			Projected: p.adjPoints,
			GW1:       p.gw1Points,
			ByGW:      p.gwPoints,
			Captain:   i == s.captain,
		}
	}

	defs, mids, fwds := 0, 0, 0
	for _, i := range s.xi {
		switch players[i].proj.Position {
		case "DEF":
			defs++
		case "MID":
			mids++
		case "FWD":
			fwds++
		}
	}

	out := squadJSON{
		Season:          season,
		Gameweeks:       gameweeks,
		Budget:          budget,
		Cost:            s.cost(players),
		Formation:       fmt.Sprintf("%d-%d-%d", defs, mids, fwds),
		ProjectedPoints: s.score(players),
		CaptainID:       players[s.captain].proj.PlayerID,
	}
	for _, i := range s.xi {
		out.XI = append(out.XI, toJSON(i))
	}
	for _, i := range s.bench {
		out.Bench = append(out.Bench, toJSON(i))
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printSquad renders the final squad.
func printSquad(players []cPlayer, s squad, budget float64, gameweeks int) {
	// Group XI by position.
	type row struct {
		name    string
		team    string
		pos     string
		points  float64
		price   float64
		captain bool
	}

	order := []string{"GK", "DEF", "MID", "FWD"}
	var xiRows []row
	for _, pos := range order {
		for _, i := range s.xi {
			if players[i].proj.Position == pos {
				xiRows = append(xiRows, row{
					name:    fmt.Sprintf("%s %s", players[i].proj.FirstName, players[i].proj.LastName),
					team:    players[i].teamName,
					pos:     pos,
					points:  players[i].adjPoints,
					price:   players[i].price,
					captain: i == s.captain,
				})
			}
		}
	}

	// Count formation for display.
	defs, mids, fwds := 0, 0, 0
	for _, i := range s.xi {
		switch players[i].proj.Position {
		case "DEF":
			defs++
		case "MID":
			mids++
		case "FWD":
			fwds++
		}
	}

	fmt.Printf("Optimised for GW 1-%d | Budget: £%.1fM\n\n", gameweeks, budget)
	fmt.Printf("Starting XI (%d-%d-%d)                        Proj   Price\n", defs, mids, fwds)
	fmt.Println("---------------------------------------------- ------  ------")
	for _, r := range xiRows {
		name := r.name
		if len(name) > 20 {
			name = name[:20]
		}
		team := r.team
		if len(team) > 18 {
			team = team[:18]
		}
		capt := " "
		if r.captain {
			capt = "C"
		}
		fmt.Printf("  %-3s %-20s %-18s %6.1f  %5.1fM %s\n",
			r.pos, name, team, r.points, r.price, capt)
	}

	fmt.Println("\nBench")
	fmt.Println("---------------------------------------------- ------  ------")
	for _, pos := range order {
		for _, i := range s.bench {
			if players[i].proj.Position == pos {
				name := fmt.Sprintf("%s %s", players[i].proj.FirstName, players[i].proj.LastName)
				if len(name) > 20 {
					name = name[:20]
				}
				team := players[i].teamName
				if len(team) > 18 {
					team = team[:18]
				}
				fmt.Printf("  %-3s %-20s %-18s %6.1f  %5.1fM\n",
					pos, name, team, players[i].adjPoints, players[i].price)
			}
		}
	}

	total := s.cost(players)
	fmt.Printf("\nSquad: £%.1fM / £%.1fM\n", total, budget)
	fmt.Printf("Projected XI pts (GW 1-%d): %.1f (incl. GW1 captain bonus: %.1f)\n",
		gameweeks, s.score(players), players[s.captain].gw1Points)
}
