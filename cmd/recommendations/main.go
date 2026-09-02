// Command recommendations displays past transfer recommendations generated
// by cmd/transfers for both the classic and draft FPL games.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mandyville/mandyville-draft/classic"
	"github.com/mandyville/mandyville-draft/draft"
	"github.com/mandyville/mandyville-draft/projection"
)

func main() {
	game := flag.String("game", "classic", "game to show: classic or draft")
	season := flag.Int("season", 2026, "season")
	runID := flag.Int("id", 0, "show a specific recommendation run by ID")
	gw := flag.Int("gw", 0, "show the latest run for this gameweek")
	limit := flag.Int("limit", 20, "max runs to list")
	leagueID := flag.Int("league", 0, "FPL draft league id (draft only)")
	entryID := flag.Int("entry", 0, "FPL classic entry id override")
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
	if *runID != 0 && *gw != 0 {
		fmt.Fprintln(os.Stderr, "use -id or -gw, not both")
		os.Exit(1)
	}

	cfg := resolveConfig(*configFile, dbHost, dbPort, dbUser, dbPass, dbName)
	db, err := projection.OpenDB(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if *game == "draft" {
		if err := runDraftMode(db, *season, *leagueID, *runID, *gw, *limit); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runClassicMode(db, *season, *entryID, *runID, *gw, *limit); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── classic ──────────────────────────────────────────────────────────────

func runClassicMode(db *sql.DB, season, entryOverride, runID, gw, limit int) error {
	if runID != 0 {
		return showClassicRun(db, runID)
	}

	entry, err := classic.LoadEntry(db, season, entryOverride)
	if err != nil {
		return err
	}

	if gw != 0 {
		return showClassicLatest(db, entry.ID, gw)
	}

	return listClassicRuns(db, entry, limit)
}

func listClassicRuns(db *sql.DB, entry *classic.Entry, limit int) error {
	runs, err := classic.LoadRecommendationRuns(db, entry.ID, limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("No classic recommendation runs found.")
		return nil
	}

	// Collect run IDs and load headline rows.
	ids := make([]int, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	headlines, err := classic.LoadRunHeadlineRows(db, ids)
	if err != nil {
		return err
	}

	// Collect all player IDs for name resolution.
	pidSet := map[int]bool{}
	for _, rows := range headlines {
		for _, r := range rows {
			if r.PlayerInID != 0 {
				pidSet[r.PlayerInID] = true
			}
			if r.PlayerOutID != 0 {
				pidSet[r.PlayerOutID] = true
			}
		}
	}
	names, err := resolveNames(db, pidSet, "classic")
	if err != nil {
		return err
	}

	fmt.Printf("=== %s · Classic Recommendations ===\n\n", entry.Name)
	fmt.Printf("  %-4s  %-2s  %-16s  %7s  %s\n", "ID", "GW", "Time", "Horizon", "Action")
	for _, r := range runs {
		action := "Roll"
		if rows, ok := headlines[r.ID]; ok && len(rows) > 0 {
			parts := make([]string, len(rows))
			for i, row := range rows {
				out := nameOrID(names, row.PlayerOutID)
				in := nameOrID(names, row.PlayerInID)
				parts[i] = fmt.Sprintf("%s → %s (%s)", out, in, row.Position)
			}
			action = strings.Join(parts, "; ")
		}
		fmt.Printf("  %-4d  %-2d  %-16s  %7d  %s\n",
			r.ID, r.Event, r.RunTime.Local().Format("2006-01-02 15:04"), r.Horizon, action)
	}
	fmt.Println()
	return nil
}

func showClassicRun(db *sql.DB, runID int) error {
	run, err := classic.LoadRecommendationRun(db, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("recommendation run %d not found", runID)
	}
	return printClassicDetail(db, run)
}

func showClassicLatest(db *sql.DB, entryID, event int) error {
	run, err := classic.LoadLatestRunForEvent(db, entryID, event)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("no classic recommendation found for gameweek %d", event)
	}
	return printClassicDetail(db, run)
}

func printClassicDetail(db *sql.DB, run *classic.RecommendationRun) error {
	// Collect all player IDs.
	pidSet := map[int]bool{}
	for _, r := range run.Rows {
		if r.PlayerInID != 0 {
			pidSet[r.PlayerInID] = true
		}
		if r.PlayerOutID != 0 {
			pidSet[r.PlayerOutID] = true
		}
	}
	names, err := resolveNames(db, pidSet, "classic")
	if err != nil {
		return err
	}

	// Group rows by gameweek.
	type gwStep struct {
		gw         int
		transfers  []classic.RecommendationRow
		captain    int
		captainPts float64
		xi         []int
		bench      []int
		points     float64 // sum of starter points
		freeUsed   int
		hits       int
	}

	stepMap := map[int]*gwStep{}
	var gwOrder []int
	for _, r := range run.Rows {
		s, ok := stepMap[r.StepEvent]
		if !ok {
			s = &gwStep{gw: r.StepEvent}
			stepMap[r.StepEvent] = s
			gwOrder = append(gwOrder, r.StepEvent)
		}
		switch r.Kind {
		case "transfer":
			s.transfers = append(s.transfers, r)
			if r.HitCost > 0 {
				s.hits += r.HitCost / 4
			} else {
				s.freeUsed++
			}
		case "captain":
			s.captain = r.PlayerInID
			s.captainPts = r.ExpectedGain
		case "start":
			s.xi = append(s.xi, r.PlayerInID)
			s.points += r.ExpectedGain
		case "bench":
			s.bench = append(s.bench, r.PlayerInID)
		}
	}

	// Add captain doubling bonus: the captain is already in the XI sum,
	// so add their points once more for the captain bonus.
	for _, s := range stepMap {
		s.points += s.captainPts
	}

	// Resolve positions from rows.
	posOf := map[int]string{}
	for _, r := range run.Rows {
		if r.Position != "" {
			if r.PlayerInID != 0 {
				posOf[r.PlayerInID] = r.Position
			}
			if r.PlayerOutID != 0 {
				posOf[r.PlayerOutID] = r.Position
			}
		}
	}

	gain := run.PlanValue - run.BaselineValue

	fmt.Printf("Run %d · Gameweek %d · %s\n", run.ID, run.Event,
		run.RunTime.Local().Format("2006-01-02 15:04"))
	fmt.Printf("Horizon %d · Beam %d · Bank £%.1fm · FTs: %d\n",
		run.Horizon, run.BeamWidth, float64(run.Bank)/10, run.FreeTransfers)
	fmt.Printf("Plan value: %.1f · Baseline (roll): %.1f · Gain: %+.1f\n\n",
		run.PlanValue, run.BaselineValue, gain)

	// Immediate action.
	first := stepMap[gwOrder[0]]
	if len(first.transfers) > 0 {
		parts := make([]string, len(first.transfers))
		for i, t := range first.transfers {
			parts[i] = fmt.Sprintf("%s → %s (%s)", nameOrID(names, t.PlayerOutID), nameOrID(names, t.PlayerInID), t.Position)
		}
		fmt.Printf("THIS WEEK: %s\n\n", strings.Join(parts, "; "))
	} else {
		fmt.Print("THIS WEEK: Roll\n\n")
	}

	// XI for the first gameweek.
	if first.captain != 0 {
		// Detect vice-captain: the second-highest projected starter.
		// We don't store VC explicitly in the rows, so leave it out unless
		// we can infer it.
		fmt.Printf("Starting XI for Gameweek %d (%.1f projected, incl. captain):\n", first.gw, first.points)
		for _, pos := range []string{"GK", "DEF", "MID", "FWD"} {
			var playerNames []string
			for _, id := range first.xi {
				if posOf[id] == pos {
					mark := ""
					if id == first.captain {
						mark = " (C)"
					}
					playerNames = append(playerNames, nameOrID(names, id)+mark)
				}
			}
			if len(playerNames) > 0 {
				fmt.Printf("  %-3s %s\n", pos, strings.Join(playerNames, ", "))
			}
		}
		if len(first.bench) > 0 {
			benchNames := make([]string, len(first.bench))
			for i, id := range first.bench {
				benchNames[i] = nameOrID(names, id)
			}
			fmt.Printf("  Bench: %s\n", strings.Join(benchNames, ", "))
		}
		fmt.Println()
	}

	// Plan table.
	fmt.Println("Plan:")
	fmt.Printf("  %-5s %-40s %5s %4s %7s\n", "GW", "Transfers", "FT", "Hits", "XI pts")
	var total float64
	for _, gw := range gwOrder {
		s := stepMap[gw]
		total += s.points - float64(s.hits)*4
		if len(s.transfers) == 0 {
			fmt.Printf("  GW%-3d %-40s %5d %4d %7.1f\n", s.gw, "roll", s.freeUsed, s.hits, s.points)
		} else {
			for i, t := range s.transfers {
				desc := fmt.Sprintf("%s → %s (%s)", nameOrID(names, t.PlayerOutID), nameOrID(names, t.PlayerInID), t.Position)
				if i == 0 {
					fmt.Printf("  GW%-3d %-40s %5d %4d %7.1f\n", s.gw, desc, s.freeUsed, s.hits, s.points)
				} else {
					fmt.Printf("  %-5s %-40s\n", "", desc)
				}
			}
		}
	}
	fmt.Printf("  %-5s %-40s %5s %4s %7.1f\n\n", "", "", "", "", total)

	return nil
}

// ── draft ────────────────────────────────────────────────────────────────

func runDraftMode(db *sql.DB, season, leagueID, runID, gw, limit int) error {
	if runID != 0 {
		return showDraftRun(db, runID)
	}

	league, entry, err := resolveDraftEntry(db, season, leagueID)
	if err != nil {
		return err
	}

	if gw != 0 {
		return showDraftLatest(db, league.ID, entry.ID, gw)
	}

	return listDraftRuns(db, league, entry, limit)
}

func resolveDraftEntry(db *sql.DB, season, leagueID int) (*draft.League, *draft.Entry, error) {
	leagues, err := draft.LoadLeagues(db, season)
	if err != nil {
		return nil, nil, err
	}

	var league *draft.League
	if leagueID != 0 {
		for i := range leagues {
			if leagues[i].FPLID == leagueID || leagues[i].ID == leagueID {
				league = &leagues[i]
				break
			}
		}
		if league == nil {
			return nil, nil, fmt.Errorf("no draft league with id %d for season %d", leagueID, season)
		}
	} else if len(leagues) == 1 {
		league = &leagues[0]
	} else if len(leagues) == 0 {
		return nil, nil, fmt.Errorf("no draft leagues found for season %d", season)
	} else {
		return nil, nil, fmt.Errorf("%d draft leagues for season %d; use -league to pick one", len(leagues), season)
	}

	entries, err := draft.LoadEntries(db, league.ID)
	if err != nil {
		return nil, nil, err
	}
	for i := range entries {
		if entries[i].IsMine {
			return league, &entries[i], nil
		}
	}
	return nil, nil, fmt.Errorf("no entry in league %s is marked is_mine", league.Name)
}

func listDraftRuns(db *sql.DB, league *draft.League, entry *draft.Entry, limit int) error {
	runs, err := draft.LoadRecentRuns(db, league.ID, entry.ID, limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("No draft recommendation runs found.")
		return nil
	}

	// Load candidates for each run to derive headlines.
	// For efficiency we only need the first recommended free-agent candidate.
	pidSet := map[int]bool{}
	type headlineInfo struct {
		outID, inID int
		position    string
		gain        float64
	}
	headlineByRun := map[int]*headlineInfo{}
	for _, r := range runs {
		cands, err := draft.LoadRunCandidates(db, r.ID)
		if err != nil {
			return err
		}
		for _, c := range cands {
			if c.Kind == "free-agent" && c.Recommended {
				headlineByRun[r.ID] = &headlineInfo{
					outID: c.PlayerOutID, inID: c.PlayerInID,
					position: c.Position, gain: c.ExpectedGain,
				}
				pidSet[c.PlayerOutID] = true
				pidSet[c.PlayerInID] = true
				break
			}
		}
	}
	names, err := resolveNames(db, pidSet, "draft")
	if err != nil {
		return err
	}

	fmt.Printf("=== %s · %s · Draft Recommendations ===\n\n", entry.Name, league.Name)
	fmt.Printf("  %-4s  %-2s  %-16s  %7s  %s\n", "ID", "GW", "Time", "Horizon", "Action")
	for _, r := range runs {
		action := "No action"
		if h, ok := headlineByRun[r.ID]; ok {
			action = fmt.Sprintf("%s → %s (%s, +%.1f)",
				nameOrID(names, h.outID), nameOrID(names, h.inID), h.position, h.gain)
		}
		fmt.Printf("  %-4d  %-2d  %-16s  %7d  %s\n",
			r.ID, r.Event, r.RunTime.Local().Format("2006-01-02 15:04"), r.Horizon, action)
	}
	fmt.Println()
	return nil
}

func showDraftRun(db *sql.DB, runID int) error {
	run, err := draft.LoadSingleRun(db, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("recommendation run %d not found", runID)
	}
	return printDraftDetail(db, run)
}

func showDraftLatest(db *sql.DB, leagueID, entryID, event int) error {
	run, err := draft.LoadLatestRunForEvent(db, leagueID, entryID, event)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("no draft recommendation found for gameweek %d", event)
	}
	return printDraftDetail(db, run)
}

func printDraftDetail(db *sql.DB, run *draft.RecommendationRun) error {
	// Collect player IDs.
	pidSet := map[int]bool{}
	for _, c := range run.Candidates {
		if c.PlayerInID != 0 {
			pidSet[c.PlayerInID] = true
		}
		if c.PlayerOutID != 0 {
			pidSet[c.PlayerOutID] = true
		}
	}
	names, err := resolveNames(db, pidSet, "draft")
	if err != nil {
		return err
	}

	fmt.Printf("Run %d · Gameweek %d · %s\n", run.ID, run.Event,
		run.RunTime.Local().Format("2006-01-02 15:04"))
	fmt.Printf("Horizon %d · Discount %.2f\n\n", run.Horizon, run.Discount)

	// Split into free-agent and waiver candidates.
	var freeAgent, waiver []draft.LoggedCandidate
	for _, c := range run.Candidates {
		switch c.Kind {
		case "free-agent":
			freeAgent = append(freeAgent, c)
		case "waiver":
			waiver = append(waiver, c)
		}
	}

	if len(freeAgent) > 0 {
		fmt.Println("Free-agent transfers:")
		fmt.Printf("  %-3s %-22s %-22s %-4s %7s %7s\n", "#", "Drop", "In", "Pos", "Gain", "H2H")
		fmt.Println("  " + strings.Repeat("-", 73))
		for i, c := range freeAgent {
			h2h := ""
			if c.H2HGain != nil {
				h2h = fmt.Sprintf("%7.2f", *c.H2HGain)
			}
			fmt.Printf("  %-3d %-22s %-22s %-4s %7.2f %s\n",
				i+1,
				truncate(nameOrID(names, c.PlayerOutID), 22),
				truncate(nameOrID(names, c.PlayerInID), 22),
				c.Position, c.ExpectedGain, h2h)
		}
		fmt.Println()
	}

	if len(waiver) > 0 {
		// Group by dropped player.
		type waiverGroup struct {
			outID    int
			position string
			claims   []draft.LoggedCandidate
		}
		groups := map[int]*waiverGroup{}
		var groupOrder []int
		for _, c := range waiver {
			g, ok := groups[c.PlayerOutID]
			if !ok {
				g = &waiverGroup{outID: c.PlayerOutID, position: c.Position}
				groups[c.PlayerOutID] = g
				groupOrder = append(groupOrder, c.PlayerOutID)
			}
			g.claims = append(g.claims, c)
		}

		fmt.Println("Waiver claims:")
		for _, outID := range groupOrder {
			g := groups[outID]
			fmt.Printf("  Drop %s (%s) for:\n", nameOrID(names, g.outID), g.position)
			for i, c := range g.claims {
				row := fmt.Sprintf("    %d. %-22s gain %5.2f", i+1,
					truncate(nameOrID(names, c.PlayerInID), 22), c.ExpectedGain)
				if c.H2HGain != nil {
					row += fmt.Sprintf("  H2H %5.2f", *c.H2HGain)
				}
				if c.SuccessProbability != nil {
					row += fmt.Sprintf("  (%.0f%% chance)", *c.SuccessProbability*100)
				}
				fmt.Println(row)
			}
		}
		fmt.Println()
	}

	if len(freeAgent) == 0 && len(waiver) == 0 {
		fmt.Println("(no recommendations logged)")
	}

	return nil
}

// ── shared helpers ───────────────────────────────────────────────────────

func resolveNames(db *sql.DB, pidSet map[int]bool, game string) (map[int]string, error) {
	ids := make([]int, 0, len(pidSet))
	for id := range pidSet {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	if game == "draft" {
		return draft.LoadPlayerNames(db, ids)
	}
	return classic.LoadPlayerNames(db, ids)
}

func nameOrID(names map[int]string, id int) string {
	if id == 0 {
		return "?"
	}
	if n, ok := names[id]; ok {
		return n
	}
	return fmt.Sprintf("player %d", id)
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

func resolveConfig(configFile string, dbHost *string, dbPort *int, dbUser, dbPass, dbName *string) projection.DBConfig {
	cfg := projection.DBConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "password",
		DBName:   "mandyville",
	}
	if configFile != "" {
		var err error
		cfg, err = projection.LoadDBConfigFromFile(configFile)
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
