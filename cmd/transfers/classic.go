package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mandyville/mandyville-draft/classic"
	"github.com/mandyville/mandyville-draft/projection"
	squadpkg "github.com/mandyville/mandyville-draft/squad"
)

// classicArgs carries the classic-mode command line parameters.
type classicArgs struct {
	season, entry, horizon, beam, maxTransfers, pairShortlist int
	minGain                                                   float64
	bank, freeTransfers                                       int
	jsonOut, input                                            string
	persist                                                   bool
}

func runClassic(db *sql.DB, writeCfg *projection.DBConfig, args classicArgs) error {
	entry, err := classic.LoadEntry(db, args.season, args.entry)
	if err != nil {
		return err
	}

	startGW, err := classic.UpcomingGameweek(db, args.season)
	if err != nil {
		return err
	}

	output, err := loadProjections(db, args.input, args.season, 1, projection.ClassicRules, startGW)
	if err != nil {
		return err
	}
	players := toPlayers(output)

	// Player -> team, derived from the projection (itself sourced from
	// players_teams), for the club limit.
	teams := map[int]projection.PlayerTeamInfo{}
	for _, p := range output.Players {
		teams[p.PlayerID] = projection.PlayerTeamInfo{TeamID: p.TeamID, TeamName: p.TeamName, IsPL: true}
	}

	byPlayer, _, err := classic.LoadElementMapping(db, args.season)
	if err != nil {
		return err
	}
	positions, err := classic.LoadPositions(db, args.season)
	if err != nil {
		return err
	}
	currentPrices, priceWarnings, err := classic.ResolveCurrentPrices(db, args.season)
	if err != nil {
		return err
	}
	startingPrices, err := classic.LoadStartingPrices(db, args.season)
	if err != nil {
		return err
	}

	pool := classic.BuildPool(players, currentPrices, byPlayer, teams)

	history, err := classic.LoadHistory(db, entry.ID)
	if err != nil {
		return err
	}
	transfers, err := classic.LoadTransfers(db, entry.ID)
	if err != nil {
		return err
	}
	chips, err := classic.LoadChips(db, entry.ID)
	if err != nil {
		return err
	}

	squad, err := classic.LoadSquad(db, entry, pool, currentPrices, startingPrices, byPlayer, positions, teams, history, transfers, chips, startGW)
	if err != nil {
		return err
	}

	if args.bank >= 0 {
		squad.Bank = args.bank
		squad.Warnings = append(squad.Warnings, fmt.Sprintf("bank overridden to %s", money(args.bank)))
	}
	if args.freeTransfers >= 0 {
		squad.FreeTransfers = args.freeTransfers
		squad.Warnings = append(squad.Warnings, fmt.Sprintf("free transfers overridden to %d", args.freeTransfers))
	}
	squad.Warnings = append(squad.Warnings, priceWarnings...)

	planner := classic.NewPlanner(pool, startGW, args.horizon, args.beam, args.maxTransfers, args.pairShortlist)
	planner.Progress = func(gw, states int, best float64, elapsed time.Duration) {
		fmt.Fprintf(os.Stderr, "GW%-2d: %4d states, best %.1f pts (%s)\n", gw, states, best, elapsed.Round(10*time.Millisecond))
	}

	outcome, err := planner.Plan(squad)
	if err != nil {
		return err
	}
	candidates := planner.ThisWeekSingles(squad)
	view := buildView(pool, squad)

	printClassic(entry, squad, view, outcome, candidates, startGW, args.horizon, args.minGain)

	if args.jsonOut != "" {
		if err := writeClassicJSON(args.jsonOut, entry, squad, view, outcome, candidates, startGW, args.horizon); err != nil {
			return err
		}
		fmt.Printf("\nFull output written to %s\n", args.jsonOut)
	}

	if writeCfg != nil {
		if err := logClassicRun(writeCfg, entry, squad, pool, outcome, startGW, args, output); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// logClassicRun persists the projection snapshot (for reproducibility) and
// the recommendation run, unless disabled.
func logClassicRun(writeCfg *projection.DBConfig, entry *classic.Entry, squad *classic.Squad, pool *classic.Pool, outcome *classic.Outcome, startGW int, args classicArgs, output *projection.ProjectionOutput) error {
	wdb, err := projection.OpenDB(*writeCfg)
	if err != nil {
		return fmt.Errorf("cannot open write connection: %w", err)
	}
	defer wdb.Close()

	var projRunID *int
	if args.persist {
		id, err := projection.SaveProjectionRun(wdb, output, nil, "classic", "cmd/transfers")
		if err != nil {
			return fmt.Errorf("persisting projection snapshot: %w", err)
		}
		projRunID = &id
	}

	run := classic.RecommendationRun{
		EntryID:         entry.ID,
		Event:           startGW,
		Horizon:         args.horizon,
		BeamWidth:       args.beam,
		MaxTransfers:    args.maxTransfers,
		MinGain:         args.minGain,
		PlanValue:       outcome.Recommended.Total,
		BaselineValue:   planTotal(outcome.RollThisWeek),
		FreeTransfers:   squad.FreeTransfers,
		Bank:            squad.Bank,
		ProjectionRunID: projRunID,
	}
	run.Build(outcome, pool, squad)

	runID, err := classic.SaveRecommendationRun(wdb, &run)
	if err != nil {
		return fmt.Errorf("logging recommendations: %w", err)
	}
	fmt.Printf("\nLogged recommendation run %d\n", runID)
	return nil
}

func planTotal(p *classic.Plan) float64 {
	if p == nil {
		return 0
	}
	return p.Total
}

// money formats tenths of millions as £x.xm.
func money(tenths int) string {
	return fmt.Sprintf("£%.1fm", float64(tenths)/10)
}

func memberName(m *classic.Member) string {
	if m == nil {
		return "?"
	}
	if m.Name != "" {
		return m.Name
	}
	if m.Player != nil && m.Player.Name != "" {
		return m.Player.Name
	}
	if m.PlayerID != 0 {
		return fmt.Sprintf("player %d", m.PlayerID)
	}
	return fmt.Sprintf("element %d", m.Element)
}

func singleMoveString(m classic.Move) string {
	return fmt.Sprintf("%s → %s (%s)", memberName(m.Out), m.In.Player.Name, m.Position)
}

func moveString(moves []classic.Move) string {
	parts := make([]string, len(moves))
	for i, m := range moves {
		parts[i] = singleMoveString(m)
	}
	return strings.Join(parts, "; ")
}

// squadValue returns the total current selling price of the squad (excludes bank).
func squadValue(squad *classic.Squad) int {
	total := 0
	for _, m := range squad.Members {
		total += m.CurrentPrice
	}
	return total
}

// squadView resolves player ids to names, positions and teams across the
// whole pool plus the current squad, so bought players can be displayed as
// readily as original squad members.
type squadView struct {
	nameByID map[int]string
	posByID  map[int]string
	teamByID map[int]string
}

func buildView(pool *classic.Pool, squad *classic.Squad) *squadView {
	v := &squadView{
		nameByID: map[int]string{},
		posByID:  map[int]string{},
		teamByID: map[int]string{},
	}
	for _, pp := range pool.ByElement {
		if pp.Player == nil {
			continue
		}
		v.nameByID[pp.PlayerID] = pp.Player.Name
		v.posByID[pp.PlayerID] = pp.Player.Position
		v.teamByID[pp.PlayerID] = pp.TeamName
	}
	for _, m := range squad.Members {
		if m.PlayerID == 0 {
			continue
		}
		if m.Name != "" {
			v.nameByID[m.PlayerID] = m.Name
		}
		if m.Position != "" {
			v.posByID[m.PlayerID] = m.Position
		}
	}
	return v
}

func (v *squadView) name(id int) string {
	if n, ok := v.nameByID[id]; ok && n != "" {
		return n
	}
	return fmt.Sprintf("player %d", id)
}

func (v *squadView) pos(id int) string {
	return v.posByID[id]
}

func printClassic(entry *classic.Entry, squad *classic.Squad, view *squadView, outcome *classic.Outcome, candidates []classic.Move, startGW, horizon int, minGain float64) {
	rec := outcome.Recommended
	roll := outcome.RollThisWeek
	gain := rec.Total - planTotal(roll)

	fmt.Printf("=== %s ===\n", entry.Name)
	fmt.Printf("Season %d · Gameweek %d · Horizon %d · Bank %s · Squad %s · Free transfers: %d\n\n",
		entry.Season, startGW, horizon, money(squad.Bank), money(squadValue(squad)), squad.FreeTransfers)

	for _, w := range squad.Warnings {
		fmt.Printf("Warning: %s\n", w)
	}
	if len(squad.Warnings) > 0 {
		fmt.Println()
	}

	// This week's decision.
	fmt.Println("THIS WEEK:")
	if len(rec.Immediate) > 0 && gain >= minGain {
		fmt.Printf("  %s  (projected +%.1f over rolling)\n\n", moveString(rec.Immediate), gain)
	} else {
		fmt.Println("  Roll (save your transfer).")
		switch {
		case len(rec.Immediate) > 0:
			fmt.Printf("  Rejected (below -min-gain): %s  (+%.1f over rolling)\n\n", moveString(rec.Immediate), gain)
		case len(candidates) > 0:
			fmt.Printf("  Best single transfer for context: %s → %s (+%.1f this week)\n\n",
				memberName(candidates[0].Out), candidates[0].In.Player.Name, candidates[0].Gain)
		default:
			fmt.Println()
		}
	}

	// Upcoming XI, bench and captain.
	if len(rec.Steps) > 0 {
		printClassicXI(rec.Steps[0], view, startGW)
	}

	// Plan table.
	printClassicPlan(rec, startGW)

	// Alternatives.
	if len(outcome.Alternatives) > 0 {
		fmt.Println("Alternatives (by this week's action):")
		for _, alt := range outcome.Alternatives {
			action := "roll"
			if len(alt.Immediate) > 0 {
				action = moveString(alt.Immediate)
			}
			fmt.Printf("  %-60s total %.1f\n", action, alt.Total)
		}
		fmt.Println()
	}
}

func printClassicXI(step classic.Step, view *squadView, gw int) {
	fmt.Printf("Starting XI for Gameweek %d (%.1f projected, incl. captain):\n", gw, step.Points)
	for _, pos := range []string{squadpkg.PosGK, squadpkg.PosDEF, squadpkg.PosMID, squadpkg.PosFWD} {
		var names []string
		for _, id := range step.XI {
			if view.pos(id) == pos {
				mark := ""
				if id == step.Captain {
					mark = " (C)"
				} else if id == step.ViceCaptain {
					mark = " (VC)"
				}
				names = append(names, view.name(id)+mark)
			}
		}
		if len(names) > 0 {
			fmt.Printf("  %-3s %s\n", pos, strings.Join(names, ", "))
		}
	}

	var bench []string
	for _, id := range step.Bench {
		bench = append(bench, view.name(id))
	}
	fmt.Printf("  Bench: %s\n\n", strings.Join(bench, ", "))
}

func printClassicPlan(plan *classic.Plan, startGW int) {
	fmt.Println("Plan (projected points, hits included):")
	fmt.Printf("  %-5s %-40s %5s %4s %7s\n", "GW", "Transfers", "FT", "Hits", "XI pts")
	for _, s := range plan.Steps {
		if len(s.Moves) == 0 {
			fmt.Printf("  GW%-3d %-40s %5d %4d %7.1f\n", s.GW, "roll", s.FreeUsed, s.Hits, s.Points)
		} else {
			for i, m := range s.Moves {
				desc := singleMoveString(m)
				if i == 0 {
					fmt.Printf("  GW%-3d %-40s %5d %4d %7.1f\n", s.GW, desc, s.FreeUsed, s.Hits, s.Points)
				} else {
					fmt.Printf("  %-5s %-40s\n", "", desc)
				}
			}
		}
	}
	fmt.Printf("  %-5s %-40s %5s %4s %7.1f\n\n", "", "", "", "", plan.Total)
}

// --- JSON output ---

type classicJSONPlayer struct {
	PlayerID      int     `json:"player_id"`
	Element       int     `json:"element"`
	Name          string  `json:"name"`
	Position      string  `json:"position"`
	Team          string  `json:"team"`
	PurchasePrice float64 `json:"purchase_price"`
	CurrentPrice  float64 `json:"current_price"`
	SellingPrice  float64 `json:"selling_price"`
}

type classicJSONMove struct {
	OutElement int     `json:"out_element"`
	OutName    string  `json:"out_name"`
	InElement  int     `json:"in_element"`
	InName     string  `json:"in_name"`
	InTeam     string  `json:"in_team"`
	Position   string  `json:"position"`
	Cost       float64 `json:"cost"`
	Proceeds   float64 `json:"proceeds"`
	Gain       float64 `json:"gain,omitempty"`
}

type classicJSONStep struct {
	GW          int                    `json:"gameweek"`
	Moves       []classicJSONMove      `json:"moves"`
	FreeUsed    int                    `json:"free_transfers_used"`
	Hits        int                    `json:"hits"`
	Points      float64                `json:"projected_points"`
	Captain     classicJSONPlayerRef   `json:"captain"`
	ViceCaptain classicJSONPlayerRef   `json:"vice_captain"`
	XI          []classicJSONPlayerRef `json:"starting_xi"`
	Bench       []classicJSONPlayerRef `json:"bench"`
}

type classicJSONPlayerRef struct {
	PlayerID int    `json:"player_id"`
	Name     string `json:"name"`
	Position string `json:"position"`
}

type classicJSON struct {
	Season           int                 `json:"season"`
	Gameweek         int                 `json:"gameweek"`
	Horizon          int                 `json:"horizon"`
	Entry            string              `json:"entry"`
	Bank             float64             `json:"bank"`
	SquadValue       float64             `json:"squad_value"`
	FreeTransfers    int                 `json:"free_transfers"`
	Warnings         []string            `json:"warnings,omitempty"`
	Squad            []classicJSONPlayer `json:"squad"`
	Recommended      []classicJSONStep   `json:"recommended_plan"`
	RecommendedTotal float64             `json:"recommended_total"`
	RollBaseline     float64             `json:"roll_baseline_total"`
	Alternatives     []classicJSONAlt    `json:"alternatives"`
	Candidates       []classicJSONMove   `json:"candidate_transfers"`
}

type classicJSONAlt struct {
	Action string  `json:"action"`
	Total  float64 `json:"total"`
}

func writeClassicJSON(path string, entry *classic.Entry, squad *classic.Squad, view *squadView, outcome *classic.Outcome, candidates []classic.Move, startGW, horizon int) error {
	out := classicJSON{
		Season:        entry.Season,
		Gameweek:      startGW,
		Horizon:       horizon,
		Entry:         entry.Name,
		Bank:          float64(squad.Bank) / 10,
		SquadValue:    float64(squadValue(squad)) / 10,
		FreeTransfers: squad.FreeTransfers,
		Warnings:      squad.Warnings,
	}

	// Squad members, sorted by position then name for stable output.
	elems := make([]int, 0, len(squad.Members))
	for e := range squad.Members {
		elems = append(elems, e)
	}
	sort.Slice(elems, func(i, j int) bool {
		a, b := squad.Members[elems[i]], squad.Members[elems[j]]
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return memberName(a) < memberName(b)
	})
	for _, e := range elems {
		m := squad.Members[e]
		out.Squad = append(out.Squad, classicJSONPlayer{
			PlayerID:      m.PlayerID,
			Element:       m.Element,
			Name:          memberName(m),
			Position:      m.Position,
			Team:          view.teamByID[m.PlayerID],
			PurchasePrice: float64(m.PurchasePrice) / 10,
			CurrentPrice:  float64(m.CurrentPrice) / 10,
			SellingPrice:  float64(classic.SellingPrice(m.PurchasePrice, m.CurrentPrice)) / 10,
		})
	}

	if rec := outcome.Recommended; rec != nil {
		out.RecommendedTotal = rec.Total
		for _, s := range rec.Steps {
			out.Recommended = append(out.Recommended, jsonStep(s, view))
		}
	}
	out.RollBaseline = planTotal(outcome.RollThisWeek)

	for _, alt := range outcome.Alternatives {
		action := "roll"
		if len(alt.Immediate) > 0 {
			action = moveString(alt.Immediate)
		}
		out.Alternatives = append(out.Alternatives, classicJSONAlt{Action: action, Total: alt.Total})
	}

	for _, m := range candidates {
		out.Candidates = append(out.Candidates, classicJSONMove{
			OutElement: m.OutElement,
			OutName:    memberName(m.Out),
			InElement:  m.InElement,
			InName:     m.In.Player.Name,
			InTeam:     m.In.TeamName,
			Position:   m.Position,
			Cost:       float64(m.Cost) / 10,
			Proceeds:   float64(m.Proceeds) / 10,
			Gain:       m.Gain,
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

func jsonStep(s classic.Step, view *squadView) classicJSONStep {
	js := classicJSONStep{
		GW: s.GW, FreeUsed: s.FreeUsed, Hits: s.Hits,
		Points:      s.Points,
		Captain:     classicJSONPlayerRef{PlayerID: s.Captain, Name: view.name(s.Captain), Position: view.pos(s.Captain)},
		ViceCaptain: classicJSONPlayerRef{PlayerID: s.ViceCaptain, Name: view.name(s.ViceCaptain), Position: view.pos(s.ViceCaptain)},
	}
	for _, id := range s.XI {
		js.XI = append(js.XI, classicJSONPlayerRef{PlayerID: id, Name: view.name(id), Position: view.pos(id)})
	}
	for _, id := range s.Bench {
		js.Bench = append(js.Bench, classicJSONPlayerRef{PlayerID: id, Name: view.name(id), Position: view.pos(id)})
	}
	for _, m := range s.Moves {
		js.Moves = append(js.Moves, classicJSONMove{
			OutElement: m.OutElement, OutName: memberName(m.Out),
			InElement: m.InElement, InName: m.In.Player.Name, InTeam: m.In.TeamName,
			Position: m.Position,
			Cost:     float64(m.Cost) / 10, Proceeds: float64(m.Proceeds) / 10,
		})
	}
	return js
}
