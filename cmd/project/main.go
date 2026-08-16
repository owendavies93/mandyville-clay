package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/mandyville/mandyville-draft/projection"
)

func main() {
	season := flag.Int("season", 2026, "target season to project")
	leagueSize := flag.Int("league-size", 8, "number of managers in draft league")
	output := flag.String("output", "projections.json", "output file path")
	backtest := flag.Bool("backtest", false, "run backtest against actual FPL points")
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

	engine := projection.NewEngine(db, *season, *leagueSize)
	engine.Backtest = *backtest
	result, err := engine.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "projection failed: %v\n", err)
		os.Exit(1)
	}

	// Print top players summary.
	fmt.Printf("Projections for season %d (%d players)\n\n", result.Season, len(result.Players))

	printTopN := 30
	if len(result.Players) < printTopN {
		printTopN = len(result.Players)
	}

	fmt.Printf("%-4s %-20s %-5s %-22s %6s %6s %6s\n",
		"Rank", "Player", "Pos", "Team", "Pts", "VORP", "H2H")
	fmt.Println("---- -------------------- ----- ---------------------- ------ ------ ------")
	for i := 0; i < printTopN; i++ {
		p := result.Players[i]
		name := fmt.Sprintf("%s %s", p.FirstName, p.LastName)
		if len(name) > 20 {
			name = name[:20]
		}
		team := p.TeamName
		if len(team) > 22 {
			team = team[:22]
		}
		fmt.Printf("%-4d %-20s %-5s %-22s %6.1f %6.1f %6.1f\n",
			i+1, name, p.Position, team, p.ProjectedPoints, p.VORP, p.H2HAdjustedPts)
	}

	if *backtest {
		fmt.Println("\n--- Backtest Results ---")
		runBacktest(cfg, *season, result)
	}

	// Write JSON output.
	f, err := os.Create(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nProjections written to %s\n", *output)
}

func runBacktest(db projection.DBConfig, season int, result *projection.ProjectionOutput) {
	// We need a raw *sql.DB for the backtest query.
	sqlDB, err := projection.OpenDB(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backtest DB error: %v\n", err)
		return
	}
	defer sqlDB.Close()

	actuals, err := projection.LoadActualFPLPoints(sqlDB, season)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load actual points: %v\n", err)
		return
	}

	if len(actuals) == 0 {
		fmt.Println("No actual FPL points data available for this season.")
		return
	}

	// Compare projected vs actual.
	type comparison struct {
		name      string
		pos       string
		team      string
		projected float64
		actual    int
		diff      float64
	}

	var comparisons []comparison
	var totalSqErr float64
	var totalAbsErr float64
	var count int

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
		totalSqErr += diff * diff
		totalAbsErr += math.Abs(diff)
		count++
	}

	if count == 0 {
		fmt.Println("No matching players for backtest.")
		return
	}

	rmse := math.Sqrt(totalSqErr / float64(count))
	mae := totalAbsErr / float64(count)

	fmt.Printf("Players compared: %d\n", count)
	fmt.Printf("RMSE: %.1f points\n", rmse)
	fmt.Printf("MAE:  %.1f points\n", mae)

	// Rank correlation: how well do we rank players?
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
