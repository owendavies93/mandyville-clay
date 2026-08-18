package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/mandyville/mandyville-draft/draft"
	"github.com/mandyville/mandyville-draft/projection"
)

func main() {
	season := flag.Int("season", 2025, "season to backtest")
	leagueSize := flag.Int("league-size", 8, "number of managers in draft league")
	rolling := flag.Bool("rolling", false, "run the rolling in-season backtest")
	grade := flag.Bool("grade-recommendations", false, "grade logged transfer recommendations against actual points")
	configFile := flag.String("config", "", "path to mandyville config.yaml")
	dbHost := flag.String("db-host", "", "database host")
	dbPort := flag.Int("db-port", 0, "database port")
	dbUser := flag.String("db-user", "", "database user")
	dbPass := flag.String("db-pass", "", "database password")
	dbName := flag.String("db-name", "", "database name")
	flag.Parse()

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

	if *grade {
		gradeRecommendations(db)
		return
	}

	if *rolling {
		runRollingBacktest(db, *season, *leagueSize)
		return
	}

	runSeasonBacktest(db, *season, *leagueSize)
}

// runSeasonBacktest projects a full season from prior-season data only and
// compares the season totals against actual FPL points.
func runSeasonBacktest(db *sql.DB, season, leagueSize int) {
	engine := projection.NewEngine(db, season, leagueSize)
	engine.Backtest = true
	engine.Rules = projection.ClassicRules

	result, err := engine.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "projection failed: %v\n", err)
		os.Exit(1)
	}

	actuals, err := projection.LoadActualFPLPoints(db, season)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load actual points: %v\n", err)
		os.Exit(1)
	}
	if len(actuals) == 0 {
		fmt.Println("No actual FPL points data available for this season.")
		return
	}

	type comparison struct {
		name      string
		pos       string
		team      string
		projected float64
		actual    int
		diff      float64
	}

	var comparisons []comparison
	var projVals, actualVals []float64
	var totalSqErr, totalAbsErr float64

	for _, p := range result.Players {
		actual, ok := actuals[p.PlayerID]
		if !ok {
			continue
		}
		diff := p.ProjectedPoints - float64(actual)
		comparisons = append(comparisons, comparison{
			name:      fmt.Sprintf("%s %s", p.FirstName, p.LastName),
			pos:       p.Position,
			team:      p.TeamName,
			projected: p.ProjectedPoints,
			actual:    actual,
			diff:      diff,
		})
		projVals = append(projVals, p.ProjectedPoints)
		actualVals = append(actualVals, float64(actual))
		totalSqErr += diff * diff
		totalAbsErr += math.Abs(diff)
	}

	if len(comparisons) == 0 {
		fmt.Println("No matching players for backtest.")
		return
	}

	count := len(comparisons)
	rmse := math.Sqrt(totalSqErr / float64(count))
	mae := totalAbsErr / float64(count)

	fmt.Printf("Season %d backtest\n", season)
	fmt.Printf("Players compared: %d\n", count)
	fmt.Printf("RMSE: %.1f points\n", rmse)
	fmt.Printf("MAE:  %.1f points\n", mae)
	fmt.Printf("Spearman rank correlation: %.3f\n", spearman(projVals, actualVals))

	// Top actual scorers vs projection.
	sort.Slice(comparisons, func(i, j int) bool {
		return comparisons[i].actual > comparisons[j].actual
	})

	fmt.Printf("\nTop 20 actual scorers vs projection:\n")
	fmt.Printf("%-4s %-20s %-5s %6s %6s %6s\n",
		"Rank", "Player", "Pos", "Actual", "Proj", "Diff")
	fmt.Println("---- -------------------- ----- ------ ------ ------")
	for i := 0; i < 20 && i < len(comparisons); i++ {
		c := comparisons[i]
		name := c.name
		if len(name) > 20 {
			name = name[:20]
		}
		fmt.Printf("%-4d %-20s %-5s %6d %6.1f %+6.1f\n",
			i+1, name, c.pos, c.actual, c.projected, c.diff)
	}

	// Biggest misses.
	sort.Slice(comparisons, func(i, j int) bool {
		return math.Abs(comparisons[i].diff) > math.Abs(comparisons[j].diff)
	})

	fmt.Printf("\nBiggest misses:\n")
	fmt.Printf("%-4s %-20s %-5s %6s %6s %6s\n",
		"", "Player", "Pos", "Actual", "Proj", "Diff")
	fmt.Println("     -------------------- ----- ------ ------ ------")
	for i := 0; i < 15 && i < len(comparisons); i++ {
		c := comparisons[i]
		name := c.name
		if len(name) > 20 {
			name = name[:20]
		}
		fmt.Printf("     %-20s %-5s %6d %6.1f %+6.1f\n",
			name, c.pos, c.actual, c.projected, c.diff)
	}
}

// runRollingBacktest replays the season: at each gameweek k it projects the
// remaining fixtures using only data available before k's deadline, then
// scores the next h gameweeks against actual points.
func runRollingBacktest(db *sql.DB, season, leagueSize int) {
	horizons := []int{1, 3, 5, 8}

	actual, err := projection.LoadActualGWPoints(db, season)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load actual gameweek points: %v\n", err)
		os.Exit(1)
	}

	// Gameweek range for the season.
	maxGW := 38
	deadlines, err := projection.LoadGameweekDeadlines(db, season)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load deadlines: %v\n", err)
		os.Exit(1)
	}
	for gw := range deadlines {
		if gw > maxGW {
			maxGW = gw
		}
	}

	type metricAcc struct {
		sumAbs, sumSq float64
		n             int
		proj, actual  []float64
	}
	type liftAcc struct {
		sum float64
		n   int
	}

	horAcc := make(map[int]*metricAcc)
	horLift := make(map[int]map[string]*liftAcc)
	for _, h := range horizons {
		horAcc[h] = &metricAcc{}
		horLift[h] = make(map[string]*liftAcc)
	}

	for k := 1; k <= maxGW; k++ {
		engine := projection.NewEngine(db, season, leagueSize)
		engine.Backtest = true
		result, err := engine.RunInSeason(k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: projection at gameweek %d failed: %v\n", k, err)
			continue
		}

		projByID := make(map[int]map[int]float64, len(result.Players))
		posByID := make(map[int]string, len(result.Players))
		for _, p := range result.Players {
			gwPts := make(map[int]float64)
			for _, fx := range p.Gameweeks {
				gwPts[fx.Gameweek] += fx.ProjectedPoints
			}
			projByID[p.PlayerID] = gwPts
			posByID[p.PlayerID] = p.Position
		}

		for _, h := range horizons {
			if k+h-1 > maxGW {
				continue
			}
			acc := horAcc[h]

			// Rows for players who actually played in the window.
			type row struct {
				pos    string
				proj   float64
				actual float64
			}
			var rows []row

			for pid, gwMap := range actual {
				actSum := 0
				played := false
				for gw := k; gw <= k+h-1; gw++ {
					if pts, ok := gwMap[gw]; ok {
						actSum += pts
						played = true
					}
				}
				if !played {
					continue
				}
				projSum := 0.0
				if gwPts, ok := projByID[pid]; ok {
					for gw := k; gw <= k+h-1; gw++ {
						projSum += gwPts[gw]
					}
				}
				rows = append(rows, row{pos: posByID[pid], proj: projSum, actual: float64(actSum)})
			}

			for _, r := range rows {
				diff := r.proj - r.actual
				acc.sumAbs += math.Abs(diff)
				acc.sumSq += diff * diff
				acc.n++
				acc.proj = append(acc.proj, r.proj)
				acc.actual = append(acc.actual, r.actual)
			}

			// Top-20 lift per position: mean actual of our top-20 projected
			// players minus the field mean for that position.
			posRows := make(map[string][]row)
			for _, r := range rows {
				posRows[r.pos] = append(posRows[r.pos], r)
			}
			for pos, prs := range posRows {
				if len(prs) == 0 {
					continue
				}
				fieldMean := 0.0
				for _, r := range prs {
					fieldMean += r.actual
				}
				fieldMean /= float64(len(prs))

				sort.Slice(prs, func(i, j int) bool { return prs[i].proj > prs[j].proj })
				topN := 20
				if topN > len(prs) {
					topN = len(prs)
				}
				topMean := 0.0
				for i := 0; i < topN; i++ {
					topMean += prs[i].actual
				}
				topMean /= float64(topN)

				la := horLift[h][pos]
				if la == nil {
					la = &liftAcc{}
					horLift[h][pos] = la
				}
				la.sum += topMean - fieldMean
				la.n++
			}
		}

		fmt.Fprintf(os.Stderr, "  gameweek %d/%d done\n", k, maxGW)
	}

	fmt.Printf("Rolling in-season backtest for season %d\n\n", season)
	for _, h := range horizons {
		acc := horAcc[h]
		if acc.n == 0 {
			fmt.Printf("Horizon %2d GW: no data\n", h)
			continue
		}
		mae := acc.sumAbs / float64(acc.n)
		rmse := math.Sqrt(acc.sumSq / float64(acc.n))
		sp := spearman(acc.proj, acc.actual)

		fmt.Printf("Horizon %2d GW: n=%5d  MAE=%.2f  RMSE=%.2f  Spearman=%.3f",
			h, acc.n, mae, rmse, sp)
		for _, pos := range []string{"GK", "DEF", "MID", "FWD"} {
			if la, ok := horLift[h][pos]; ok && la.n > 0 {
				fmt.Printf("  %s+%.2f", pos, la.sum/float64(la.n))
			}
		}
		fmt.Println()
	}
}

// gradeRecommendations scores every logged transfer recommendation against
// actual points over the horizon it claimed, answering "would the swap have
// beaten holding?". Free-agent and waiver rows are graded separately.
func gradeRecommendations(db *sql.DB) {
	runs, err := draft.LoadRecommendationRuns(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load recommendations: %v\n", err)
		os.Exit(1)
	}
	if len(runs) == 0 {
		fmt.Println("No logged recommendation runs.")
		return
	}

	actualCache := map[int]map[int]map[int]int{}
	getActual := func(season int) map[int]map[int]int {
		if a, ok := actualCache[season]; ok {
			return a
		}
		a, err := projection.LoadActualGWPoints(db, season)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load actual points for %d: %v\n", season, err)
			os.Exit(1)
		}
		actualCache[season] = a
		return a
	}

	type agg struct {
		n                             int
		sumExp, sumAct, sumAbs, sumSq float64
		hits                          int
		sumProb                       float64
		nProb                         int
	}
	byKind := map[string]*agg{}

	var totalCands int
	for _, r := range runs {
		actual := getActual(r.Season)
		for _, c := range r.Candidates {
			if !c.Recommended {
				continue
			}
			in := sumActualWindow(actual, c.PlayerInID, r.Event, r.Horizon)
			out := sumActualWindow(actual, c.PlayerOutID, r.Event, r.Horizon)
			actGain := float64(in - out)
			err := c.ExpectedGain - actGain

			a := byKind[c.Kind]
			if a == nil {
				a = &agg{}
				byKind[c.Kind] = a
			}
			a.n++
			a.sumExp += c.ExpectedGain
			a.sumAct += actGain
			a.sumAbs += math.Abs(err)
			a.sumSq += err * err
			if actGain > 0 {
				a.hits++
			}
			if c.SuccessProbability != nil {
				a.sumProb += *c.SuccessProbability
				a.nProb++
			}
			totalCands++
		}
	}

	fmt.Printf("Recommendation grading: %d runs, %d candidates\n\n", len(runs), totalCands)
	fmt.Printf("%-12s %5s %9s %9s %7s %7s %7s %8s\n",
		"Kind", "n", "ExpGain", "ActGain", "MAE", "RMSE", "Hit%", "MeanProb")
	fmt.Println("------------ ----- --------- --------- ------- ------- ------- --------")
	for _, kind := range []string{"free-agent", "waiver"} {
		a := byKind[kind]
		if a == nil || a.n == 0 {
			continue
		}
		meanExp := a.sumExp / float64(a.n)
		meanAct := a.sumAct / float64(a.n)
		mae := a.sumAbs / float64(a.n)
		rmse := math.Sqrt(a.sumSq / float64(a.n))
		hit := 100 * float64(a.hits) / float64(a.n)
		meanProb := "-"
		if a.nProb > 0 {
			meanProb = fmt.Sprintf("%.2f", a.sumProb/float64(a.nProb))
		}
		fmt.Printf("%-12s %5d %9.2f %9.2f %7.2f %7.2f %6.0f%% %8s\n",
			kind, a.n, meanExp, meanAct, mae, rmse, hit, meanProb)
	}

	// Per-run summary.
	fmt.Printf("\n%-6s %-10s %-6s %-5s %8s %8s %8s\n",
		"Run", "Date", "Event", "Horiz", "Cands", "ExpGain", "ActGain")
	for _, r := range runs {
		actual := getActual(r.Season)
		var exp, act float64
		n := 0
		for _, c := range r.Candidates {
			if !c.Recommended {
				continue
			}
			in := sumActualWindow(actual, c.PlayerInID, r.Event, r.Horizon)
			out := sumActualWindow(actual, c.PlayerOutID, r.Event, r.Horizon)
			exp += c.ExpectedGain
			act += float64(in - out)
			n++
		}
		if n == 0 {
			continue
		}
		fmt.Printf("%-6d %-10s %-6d %-5d %8d %8.2f %8.2f\n",
			r.ID, r.RunTime.Format("2006-01-02"), r.Event, r.Horizon, n, exp/float64(n), act/float64(n))
	}
}

// sumActualWindow sums a player's actual FPL points over a gameweek window.
func sumActualWindow(actual map[int]map[int]int, playerID, startGW, horizon int) int {
	gwMap := actual[playerID]
	if gwMap == nil {
		return 0
	}
	total := 0
	for gw := startGW; gw < startGW+horizon; gw++ {
		total += gwMap[gw]
	}
	return total
}

// --- metrics helpers ---

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// rank returns average ranks (1..n) for a slice, resolving ties.
func rank(vals []float64) []float64 {
	n := len(vals)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return vals[idx[a]] < vals[idx[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j < n && vals[idx[j]] == vals[idx[i]] {
			j++
		}
		// Average rank over the tied block, 1-indexed.
		avg := (float64(i+j-1) / 2.0) + 1.0
		for k := i; k < j; k++ {
			ranks[idx[k]] = avg
		}
		i = j
	}
	return ranks
}

func pearson(x, y []float64) float64 {
	n := len(x)
	if n < 2 {
		return 0
	}
	mx, my := mean(x), mean(y)
	var sxy, sxx, syy float64
	for i := 0; i < n; i++ {
		dx := x[i] - mx
		dy := y[i] - my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}

func spearman(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	return pearson(rank(x), rank(y))
}
