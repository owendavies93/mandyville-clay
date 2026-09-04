// Command transfers recommends FPL transfers for the classic and draft
// games. Classic mode plans a budget-constrained transfer sequence over a
// multi-gameweek horizon; draft mode recommends same-position swaps, waiver
// claims and the starting XI from the current league state.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mandyville/mandyville-draft/draft"
	"github.com/mandyville/mandyville-draft/projection"
	"github.com/mandyville/mandyville-draft/squad"
)

func main() {
	game := flag.String("game", "classic", "game to advise: classic or draft")
	leagueID := flag.Int("league", 0, "FPL draft league id (draft only)")
	entryID := flag.Int("entry", 0, "FPL classic entry id override (classic only)")
	season := flag.Int("season", 2026, "season")
	horizon := flag.Int("horizon", 0, "number of gameweeks to project ahead (0 = game default: classic 8, draft 3)")
	discount := flag.Float64("discount", 0.9, "per-gameweek discount applied to later gameweeks (draft only)")
	top := flag.Int("top", 10, "number of candidates to show (draft)")
	minGain := flag.Float64("min-gain", 0, "minimum gain over rolling to recommend acting (0 = game default: classic 2.0, draft 1.0)")
	beam := flag.Int("beam", 200, "beam width (effective cap up to 2x to preserve alternatives)")
	maxTransfers := flag.Int("max-transfers", 2, "max transfers considered per gameweek (classic)")
	pairShortlist := flag.Int("pair-shortlist", 30, "top single moves to pair up (classic)")
	bankOverride := flag.Int("bank", -1, "override the reconstructed bank in tenths (classic)")
	ftOverride := flag.Int("free-transfers", -1, "override the reconstructed free transfers (classic)")
	persist := flag.Bool("persist", true, "persist the projection snapshot for reproducibility (classic)")
	jsonOut := flag.String("json", "", "write the full output to this file as JSON")
	input := flag.String("input", "", "reuse a projections JSON file instead of computing in-process")
	noLog := flag.Bool("no-log", false, "do not write recommendations to the database")
	configFile := flag.String("config", "", "path to mandyville config.yaml")
	dbHost := flag.String("db-host", "", "database host")
	dbPort := flag.Int("db-port", 0, "database port")
	dbUser := flag.String("db-user", "", "database user")
	dbPass := flag.String("db-pass", "", "database password")
	dbName := flag.String("db-name", "", "database name")
	flag.Parse()

	if *game != "classic" && *game != "draft" {
		fmt.Fprintln(os.Stderr, "-game must be classic or draft")
		os.Exit(1)
	}
	if *game == "draft" && *leagueID == 0 {
		fmt.Fprintln(os.Stderr, "-league is required for draft mode")
		os.Exit(1)
	}
	if *horizon < 0 {
		fmt.Fprintln(os.Stderr, "-horizon must be at least 0")
		os.Exit(1)
	}

	if *horizon == 0 {
		if *game == "draft" {
			*horizon = 3
		} else {
			*horizon = 8
		}
	}
	if *minGain == 0 {
		if *game == "draft" {
			*minGain = 1.0
		} else {
			*minGain = 2.0
		}
	}

	cfg := resolveConfig(*configFile, dbHost, dbPort, dbUser, dbPass, dbName, false)
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

	var writeCfg *projection.DBConfig
	if !*noLog {
		w := resolveConfig(*configFile, dbHost, dbPort, dbUser, dbPass, dbName, true)
		writeCfg = &w
	}

	if *game == "draft" {
		if err := runDraft(db, writeCfg, *leagueID, *season, *horizon, *top, *discount, *minGain, *jsonOut, *input); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runClassic(db, writeCfg, classicArgs{
		season:        *season,
		entry:         *entryID,
		horizon:       *horizon,
		beam:          *beam,
		maxTransfers:  *maxTransfers,
		pairShortlist: *pairShortlist,
		minGain:       *minGain,
		bank:          *bankOverride,
		freeTransfers: *ftOverride,
		jsonOut:       *jsonOut,
		input:         *input,
		persist:       *persist,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func resolveConfig(configFile string, dbHost *string, dbPort *int, dbUser, dbPass, dbName *string, write bool) projection.DBConfig {
	cfg := projection.DBConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "password",
		DBName:   "mandyville",
	}
	if configFile != "" {
		var err error
		if write {
			cfg, err = projection.LoadWriteDBConfigFromFile(configFile)
		} else {
			cfg, err = projection.LoadDBConfigFromFile(configFile)
		}
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
	return cfg
}

func runDraft(db *sql.DB, writeCfg *projection.DBConfig, leagueID, season, horizon, top int, discount, minGain float64, jsonOut, input string) error {
	league, err := findLeague(db, season, leagueID)
	if err != nil {
		return err
	}

	entries, err := draft.LoadEntries(db, league.ID)
	if err != nil {
		return err
	}
	my := findMine(entries)
	if my == nil {
		return fmt.Errorf("no entry in league %d is marked is_mine; check the fpl_draft config entry", leagueID)
	}

	ownership, err := draft.LoadOwnership(db, league.ID)
	if err != nil {
		return err
	}

	startGW, err := upcomingGameweek(db, season)
	if err != nil {
		return err
	}

	output, err := loadProjections(db, input, season, len(entries), projection.DraftRules, startGW)
	if err != nil {
		return err
	}
	pool := toPlayers(output)

	elementByPlayer := map[int]int{}
	myRoster := map[int]*draft.Player{}
	freeAgents := map[int]*draft.Player{}
	rivalRosters := map[int]map[int]*draft.Player{}
	var unmatchedFree, unprojectedFree, myUnmatched []draft.Ownership

	for _, o := range ownership {
		if o.PlayerID != 0 {
			elementByPlayer[o.PlayerID] = o.Element
		}
		switch {
		case o.EntryID == 0:
			if o.InTrade {
				continue
			}
			if o.PlayerID == 0 {
				unmatchedFree = append(unmatchedFree, o)
				continue
			}
			p, ok := pool[o.PlayerID]
			if !ok {
				unprojectedFree = append(unprojectedFree, o)
				continue
			}
			freeAgents[o.PlayerID] = p
		case o.EntryID == my.ID:
			if o.PlayerID == 0 {
				myUnmatched = append(myUnmatched, o)
				continue
			}
			if p, ok := pool[o.PlayerID]; ok {
				myRoster[o.PlayerID] = p
			}
		default:
			if o.PlayerID == 0 {
				continue
			}
			if p, ok := pool[o.PlayerID]; ok {
				if rivalRosters[o.EntryID] == nil {
					rivalRosters[o.EntryID] = map[int]*draft.Player{}
				}
				rivalRosters[o.EntryID][o.PlayerID] = p
			}
		}
	}

	var rivals []draft.RivalSquad
	for _, e := range entries {
		if e.ID == my.ID {
			continue
		}
		if r, ok := rivalRosters[e.ID]; ok {
			rivals = append(rivals, draft.RivalSquad{Entry: e, Roster: r})
		}
	}

	dead, err := deadSlots(db, season, myRoster, elementByPlayer, startGW)
	if err != nil {
		return err
	}

	candidates := draft.EvaluateSwaps(myRoster, freeAgents, dead, startGW, horizon, discount)

	order, err := draft.LoadWaiverOrder(db, league.ID)
	if err != nil {
		return err
	}
	probs := draft.ClaimProbabilities(draft.DefaultWaiverModel(), order, my.ID, rivals, freeAgents, startGW, horizon, discount)

	recommended, deadFills := selectCandidates(candidates, minGain, top)
	for i := range recommended {
		recommended[i].SuccessProb = floatPtr(probs[recommended[i].In.ID])
	}
	for i := range deadFills {
		deadFills[i].SuccessProb = floatPtr(probs[deadFills[i].In.ID])
	}
	assignClaimOrder(recommended)

	fmt.Printf("=== %s ===\n", my.Name)
	fmt.Printf("League: %s · Season %d · Gameweek %d · Horizon %d (discount %.2f)\n\n",
		league.Name, season, startGW, horizon, discount)

	printXI(myRoster, startGW)
	printXIHorizon(myRoster, startGW, horizon)

	printDeadSlots(myRoster, dead, deadFills, startGW)
	printCandidates("Free-agent transfers (act now)", recommended, startGW)
	printWaiverClaims("Waiver claims (in priority order)", recommended, startGW)
	if len(recommended) == 0 && len(deadFills) == 0 {
		fmt.Println("\nNo swaps clear the minimum gain threshold — hold.")
	}

	printUnmatched(db, season, unmatchedFree, myUnmatched, unprojectedFree)
	printLineupDiff(db, league.ID, my.ID, startGW, myRoster)

	if jsonOut != "" {
		if err := writeJSON(jsonOut, candidates, probs, elementByPlayer); err != nil {
			return err
		}
		fmt.Printf("\nFull candidate set written to %s\n", jsonOut)
	}

	if writeCfg != nil {
		wdb, err := projection.OpenDB(*writeCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot open write connection, skipping logging: %v\n", err)
			return nil
		}
		defer wdb.Close()

		// Dead-slot fills are advice too, so they are graded alongside the
		// threshold-cleared swaps.
		logged := make([]draft.Candidate, 0, len(recommended)+len(deadFills))
		logged = append(logged, recommended...)
		logged = append(logged, deadFills...)

		logRun := buildLogRun(league.ID, my.ID, startGW, horizon, discount, logged, elementByPlayer)
		runID, err := draft.SaveRecommendationRun(wdb, &logRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to log recommendations: %v\n", err)
			return nil
		}
		fmt.Printf("\nLogged recommendation run %d\n", runID)
	}
	return nil
}

// findLeague resolves a league by its FPL draft id (or, as a fallback, its
// internal id) for a season.
func findLeague(db *sql.DB, season, leagueID int) (*draft.League, error) {
	leagues, err := draft.LoadLeagues(db, season)
	if err != nil {
		return nil, err
	}
	for i := range leagues {
		if leagues[i].FPLID == leagueID {
			return &leagues[i], nil
		}
	}
	for i := range leagues {
		if leagues[i].ID == leagueID {
			return &leagues[i], nil
		}
	}
	return nil, fmt.Errorf("no draft league with id %d for season %d", leagueID, season)
}

func findMine(entries []draft.Entry) *draft.Entry {
	for i := range entries {
		if entries[i].IsMine {
			return &entries[i]
		}
	}
	return nil
}

// upcomingGameweek returns the first gameweek whose deadline is in the
// future.
func upcomingGameweek(db *sql.DB, season int) (int, error) {
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

// loadProjections computes projections in-process, or loads them from a
// JSON file if one is given.
func loadProjections(db *sql.DB, input string, season, leagueSize int, rules projection.ScoringRules, startGW int) (*projection.ProjectionOutput, error) {
	if input != "" {
		f, err := os.Open(input)
		if err != nil {
			return nil, fmt.Errorf("opening projections file: %w", err)
		}
		defer f.Close()
		var out projection.ProjectionOutput
		if err := json.NewDecoder(f).Decode(&out); err != nil {
			return nil, fmt.Errorf("parsing projections file: %w", err)
		}
		if out.Season != season {
			return nil, fmt.Errorf("projections file is for season %d, want %d", out.Season, season)
		}
		return &out, nil
	}

	engine := projection.NewEngine(db, season, leagueSize)
	engine.Rules = rules
	out, err := engine.RunInSeason(startGW)
	if err != nil {
		return nil, fmt.Errorf("projection failed: %w", err)
	}
	return out, nil
}

// toPlayers converts a projection output into squad.Player values keyed by
// player id, summing per-gameweek points across fixtures.
func toPlayers(out *projection.ProjectionOutput) map[int]*squad.Player {
	pool := make(map[int]*squad.Player, len(out.Players))
	for i := range out.Players {
		p := &out.Players[i]
		gw := map[int]float64{}
		for _, fx := range p.Gameweeks {
			gw[fx.Gameweek] += fx.ProjectedPoints
		}
		pool[p.PlayerID] = &squad.Player{
			ID:          p.PlayerID,
			Name:        strings.TrimSpace(p.FirstName + " " + p.LastName),
			Position:    p.Position,
			GWPoints:    gw,
			Consistency: p.Consistency,
		}
	}
	return pool
}

// deadFillsPerSlot is how many replacements to list for each dead slot.
const deadFillsPerSlot = 3

// deadSlots returns the set of my players whose roster slot is worthless for
// the rest of the season: the draft game has them gone (sold, loaned out or
// unregistered) and the engine projects them nothing from here on. Holding
// such a player costs a slot every remaining gameweek, which a horizon-bound
// marginal-XI gain cannot express.
func deadSlots(db *sql.DB, season int, roster map[int]*draft.Player, elementByPlayer map[int]int, startGW int) (map[int]bool, error) {
	var elems []int
	playerByElement := map[int]int{}
	for id := range roster {
		if el, ok := elementByPlayer[id]; ok {
			elems = append(elems, el)
			playerByElement[el] = id
		}
	}

	avail, err := draft.LoadElementAvailability(db, season, elems)
	if err != nil {
		return nil, err
	}

	dead := map[int]bool{}
	for el, a := range avail {
		status := strings.TrimSpace(a.Status)
		if status != "u" && status != "n" {
			continue
		}
		id := playerByElement[el]
		if draft.RestOfSeason(roster[id], startGW) > 0 {
			continue
		}
		dead[id] = true
	}
	return dead, nil
}

// selectCandidates splits the ranked candidates into normal recommendations
// and dead-slot replacements. Normal swaps are thresholded on the marginal
// XI gain. Dead-slot swaps bypass the threshold entirely: the slot is
// already worth nothing, so any positive replacement is an improvement, and
// they are ranked by rest-of-season points rather than by horizon gain
// because the horizon is exactly what fails to see the cost.
//
// H2HGain is deliberately not the threshold or the sort key. It is a
// floor-difference measure taken at player rather than XI level, which
// overstates the variance effect several-fold; its sign is unconditional
// even though a head-to-head manager wants less variance only when
// favoured; and Consistency correlates about +0.56 with projected points,
// so penalising it partly just penalises good players. In practice the
// adjustment exceeds the gain itself for the large majority of candidates,
// which would leave it deciding the recommendation. It is reported for
// context instead.
func selectCandidates(cands []draft.Candidate, minGain float64, top int) (normal, deadFills []draft.Candidate) {
	for _, c := range cands {
		if c.DeadSlot {
			deadFills = append(deadFills, c)
			continue
		}
		if c.Gain < minGain {
			continue
		}
		if len(normal) < top {
			normal = append(normal, c)
		}
	}

	sort.SliceStable(deadFills, func(i, j int) bool {
		return deadFills[i].ROSGain() > deadFills[j].ROSGain()
	})
	kept := map[int]int{}
	var trimmed []draft.Candidate
	for _, c := range deadFills {
		if kept[c.Out.ID] >= deadFillsPerSlot {
			continue
		}
		kept[c.Out.ID]++
		trimmed = append(trimmed, c)
	}
	return normal, trimmed
}

// printDeadSlots reports rostered players who will not score again this
// season, with the best available replacements.
func printDeadSlots(roster map[int]*draft.Player, dead map[int]bool, fills []draft.Candidate, startGW int) {
	if len(dead) == 0 {
		return
	}

	byOut := map[int][]draft.Candidate{}
	for _, c := range fills {
		byOut[c.Out.ID] = append(byOut[c.Out.ID], c)
	}

	ids := make([]int, 0, len(dead))
	for id := range dead {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return roster[ids[i]].Name < roster[ids[j]].Name })

	fmt.Println("Dead roster slots (drop regardless of gain):")
	for _, id := range ids {
		p := roster[id]
		fmt.Printf("  %s (%s) — 0 projected points for the rest of the season\n", p.Name, p.Position)
		cs := byOut[id]
		if len(cs) == 0 {
			fmt.Println("    no free agent in this position projects any points")
			continue
		}
		for i, c := range cs {
			row := fmt.Sprintf("    %d. %-22s ROS %6.1f  gain %5.2f  H2H %5.2f",
				i+1, truncate(c.In.Name, 22), c.InROS, c.Gain, c.H2HGain)
			if c.SuccessProb != nil {
				row += fmt.Sprintf("  (%.0f%% chance)", *c.SuccessProb*100)
			}
			fmt.Println(row)
		}
	}
	fmt.Println()
}

// assignClaimOrder groups candidates by their dropped player and assigns
// each a fallback order within its group (1 = primary claim).
func assignClaimOrder(cands []draft.Candidate) {
	groups := map[int][]int{}
	for i, c := range cands {
		groups[c.Out.ID] = append(groups[c.Out.ID], i)
	}
	for _, idxs := range groups {
		sort.Slice(idxs, func(a, b int) bool {
			return cands[idxs[a]].Gain > cands[idxs[b]].Gain
		})
		for i, idx := range idxs {
			cands[idx].ClaimOrder = i + 1
		}
	}
}

// buildLogRun builds the database rows for a recommendation run. Each
// candidate is logged once as a free-agent move and once as a waiver claim
// so both views can be graded separately.
func buildLogRun(leagueID, entryID, event, horizon int, discount float64, cands []draft.Candidate, elementByPlayer map[int]int) draft.RecommendationRun {
	run := draft.RecommendationRun{
		LeagueID: leagueID,
		EntryID:  entryID,
		Event:    event,
		Horizon:  horizon,
		Discount: discount,
	}
	for _, c := range cands {
		base := draft.LoggedCandidate{
			PlayerInID:       c.In.ID,
			PlayerOutID:      c.Out.ID,
			ElementIn:        elementByPlayer[c.In.ID],
			ElementOut:       elementByPlayer[c.Out.ID],
			Position:         c.Position,
			ExpectedGain:     c.Gain,
			UndiscountedGain: c.Undiscounted,
			H2HGain:          floatPtr(c.H2HGain),
			Recommended:      true,
		}

		fa := base
		fa.Kind = "free-agent"
		run.Candidates = append(run.Candidates, fa)

		wv := base
		wv.Kind = "waiver"
		wv.SuccessProbability = c.SuccessProb
		if c.ClaimOrder > 0 {
			order := c.ClaimOrder
			wv.ClaimOrder = &order
		}
		run.Candidates = append(run.Candidates, wv)
	}
	return run
}

func floatPtr(v float64) *float64 { return &v }

// gwPts returns a player's projected points for a gameweek, 0 if unknown.
func gwPts(p *draft.Player, gw int) float64 {
	if p.GWPoints == nil {
		return 0
	}
	return p.GWPoints[gw]
}

// --- output helpers ---

// printXI prints the recommended starting XI and bench for a gameweek.
func printXI(roster map[int]*draft.Player, gw int) {
	sel, total := draft.BestXI(roster, gw)
	if len(sel) == 0 {
		fmt.Printf("Starting XI for Gameweek %d: no projectable squad available\n", gw)
		return
	}

	selSet := map[int]bool{}
	for _, id := range sel {
		selSet[id] = true
	}

	fmt.Printf("Starting XI for Gameweek %d (%.1f projected):\n", gw, total)
	for _, pos := range []string{draft.PosGK, draft.PosDEF, draft.PosMID, draft.PosFWD} {
		var names []string
		for _, id := range sel {
			if roster[id].Position == pos {
				names = append(names, fmt.Sprintf("%s (%.1f)", roster[id].Name, gwPts(roster[id], gw)))
			}
		}
		if len(names) > 0 {
			fmt.Printf("  %-3s %s\n", pos, strings.Join(names, ", "))
		}
	}

	var bench []string
	for id, p := range roster {
		if !selSet[id] {
			bench = append(bench, fmt.Sprintf("%s (%.1f)", p.Name, gwPts(p, gw)))
		}
	}
	sort.Strings(bench)
	fmt.Printf("  Bench: %s\n\n", strings.Join(bench, ", "))
}

// printXIHorizon prints a compact XI-per-gameweek summary across the horizon.
func printXIHorizon(roster map[int]*draft.Player, startGW, horizon int) {
	fmt.Println("XI across the horizon:")
	for i := 0; i < horizon; i++ {
		gw := startGW + i
		sel, total := draft.BestXI(roster, gw)
		counts := map[string]int{}
		for _, id := range sel {
			counts[roster[id].Position]++
		}
		fmt.Printf("  GW%-2d %d-%d-%d  %.1f pts\n",
			gw, counts[draft.PosDEF], counts[draft.PosMID], counts[draft.PosFWD], total)
	}
	fmt.Println()
}

// printCandidates prints a flat ranked candidate table (the free-agent
// "act now" view).
func printCandidates(title string, cands []draft.Candidate, startGW int) {
	fmt.Println(title + ":")
	if len(cands) == 0 {
		fmt.Println("  (none)")
		return
	}

	header := fmt.Sprintf("  %-3s %-22s %-22s %-4s %7s %7s %7s", "#", "Drop", "In", "Pos", "Gain", "H2H", "ROS")
	for i := 0; i < len(cands[0].PerGW); i++ {
		header += fmt.Sprintf(" %6s", fmt.Sprintf("GW%d", startGW+i))
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)+6))

	for i, c := range cands {
		drop := truncate(c.Out.Name, 22)
		in := truncate(c.In.Name, 22)
		row := fmt.Sprintf("  %-3d %-22s %-22s %-4s %7.2f %7.2f %+7.1f",
			i+1, drop, in, c.Position, c.Gain, c.H2HGain, c.ROSGain())
		for _, g := range c.PerGW {
			row += fmt.Sprintf(" %6.2f", g)
		}
		fmt.Println(row)
	}
	fmt.Println()
}

// printWaiverClaims prints the waiver view grouped by the dropped player,
// so each group is a ready-to-submit claim list (primary claim, then
// fallbacks) with the modelled chance each target survives to my turn.
func printWaiverClaims(title string, cands []draft.Candidate, startGW int) {
	fmt.Println(title + ":")
	if len(cands) == 0 {
		fmt.Println("  (none)")
		return
	}

	groups := map[int][]draft.Candidate{}
	var order []int
	for _, c := range cands {
		if _, ok := groups[c.Out.ID]; !ok {
			order = append(order, c.Out.ID)
		}
		groups[c.Out.ID] = append(groups[c.Out.ID], c)
	}
	// Order groups by their best (first) candidate's gain, descending.
	sort.Slice(order, func(i, j int) bool {
		return groups[order[i]][0].Gain > groups[order[j]][0].Gain
	})

	for _, outID := range order {
		cs := groups[outID]
		fmt.Printf("  Drop %s (%s) for:\n", cs[0].Out.Name, cs[0].Position)
		for i, c := range cs {
			row := fmt.Sprintf("    %d. %-22s gain %5.2f  H2H %5.2f  ROS %+6.1f",
				i+1, truncate(c.In.Name, 22), c.Gain, c.H2HGain, c.ROSGain())
			for _, g := range c.PerGW {
				row += fmt.Sprintf("  %5.2f", g)
			}
			if c.SuccessProb != nil {
				row += fmt.Sprintf("  (%.0f%% chance)", *c.SuccessProb*100)
			}
			fmt.Println(row)
		}
	}
	fmt.Println()
}

// printLineupDiff compares the lineup last synced from the API against the
// recommended XI, flagging players the API lineup benches or starts
// differently.
func printLineupDiff(db *sql.DB, leagueID, entryID, gw int, roster map[int]*draft.Player) {
	picks, err := draft.LoadEntryPicks(db, leagueID, gw)
	if err != nil || len(picks[entryID]) == 0 {
		return
	}

	apiStarting := map[int]bool{}
	for _, p := range picks[entryID] {
		if p.PlayerID != 0 && p.IsStarting {
			apiStarting[p.PlayerID] = true
		}
	}

	sel, _ := draft.BestXI(roster, gw)
	recStarting := map[int]bool{}
	for _, id := range sel {
		recStarting[id] = true
	}

	var diffs []string
	for id, p := range roster {
		rec := recStarting[id]
		api, seen := apiStarting[id]
		if !seen {
			continue
		}
		switch {
		case rec && !api:
			diffs = append(diffs, fmt.Sprintf("start %s (API has them benched)", p.Name))
		case !rec && api:
			diffs = append(diffs, fmt.Sprintf("bench %s (API starts them)", p.Name))
		}
	}

	if len(diffs) == 0 {
		fmt.Println("Lineup matches the one last synced from the API.")
		return
	}
	fmt.Println("Lineup differs from the last synced API lineup:")
	for _, d := range diffs {
		fmt.Printf("  - %s\n", d)
	}
}

// jsonCandidate is the JSON-serialisable form of a candidate swap.
type jsonCandidate struct {
	Drop         string    `json:"drop"`
	DropID       int       `json:"drop_id"`
	In           string    `json:"in"`
	InID         int       `json:"in_id"`
	Position     string    `json:"position"`
	Gain         float64   `json:"gain"`
	Undiscounted float64   `json:"undiscounted_gain"`
	H2HGain      float64   `json:"h2h_gain"`
	ROSGain      float64   `json:"ros_gain"`
	DeadSlot     bool      `json:"dead_slot"`
	SuccessProb  *float64  `json:"success_probability,omitempty"`
	PerGW        []float64 `json:"per_gameweek"`
	ElementIn    int       `json:"element_in"`
	ElementOut   int       `json:"element_out"`
}

func writeJSON(path string, cands []draft.Candidate, probs map[int]float64, elementByPlayer map[int]int) error {
	out := make([]jsonCandidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, jsonCandidate{
			Drop:         c.Out.Name,
			DropID:       c.Out.ID,
			In:           c.In.Name,
			InID:         c.In.ID,
			Position:     c.Position,
			Gain:         c.Gain,
			Undiscounted: c.Undiscounted,
			H2HGain:      c.H2HGain,
			ROSGain:      c.ROSGain(),
			DeadSlot:     c.DeadSlot,
			SuccessProb:  floatPtr(probs[c.In.ID]),
			PerGW:        c.PerGW,
			ElementIn:    elementByPlayer[c.In.ID],
			ElementOut:   elementByPlayer[c.Out.ID],
		})
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
