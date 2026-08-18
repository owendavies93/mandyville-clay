package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mandyville/mandyville-draft/projection"
)

func main() {
	season := flag.Int("season", 2026, "target season to project")
	leagueSize := flag.Int("league-size", 8, "number of managers in draft league")
	output := flag.String("output", "projections.json", "output file path")
	rules := flag.String("rules", "classic", "scoring rules: classic or draft")
	asOfGW := flag.Int("as-of-gameweek", 0, "project from this gameweek onward using in-season data (0 = pre-season)")
	persist := flag.Bool("persist", false, "save the projection run to the database (requires write access)")
	configFile := flag.String("config", "", "path to mandyville config.yaml")
	dbHost := flag.String("db-host", "", "database host")
	dbPort := flag.Int("db-port", 0, "database port")
	dbUser := flag.String("db-user", "", "database user")
	dbPass := flag.String("db-pass", "", "database password")
	dbName := flag.String("db-name", "", "database name")
	flag.Parse()

	scoring := projection.ClassicRules
	switch strings.ToLower(*rules) {
	case "classic":
		scoring = projection.ClassicRules
	case "draft":
		scoring = projection.DraftRules
	default:
		fmt.Fprintf(os.Stderr, "-rules must be 'classic' or 'draft', got %q\n", *rules)
		os.Exit(1)
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

	engine := projection.NewEngine(db, *season, *leagueSize)
	engine.Rules = scoring

	var result *projection.ProjectionOutput
	if *asOfGW > 0 {
		result, err = engine.RunInSeason(*asOfGW)
	} else {
		result, err = engine.Run()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "projection failed: %v\n", err)
		os.Exit(1)
	}

	printSummary(result)

	if *persist {
		wcfg := resolveConfig(*configFile, dbHost, dbPort, dbUser, dbPass, dbName, true)
		wdb, err := projection.OpenDB(wcfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open database for writing: %v\n", err)
			os.Exit(1)
		}
		defer wdb.Close()

		runID, err := projection.SaveProjectionRun(wdb, result, engine.PriorRates(), scoring.Name, projection.EngineVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to persist projection run: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nPersisted projection run %d (%s, as-of gameweek %d)\n", runID, scoring.Name, result.AsOfGameweek)
	}

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

// resolveConfig builds a DBConfig from config file + flag overrides. When
// write is true and a config file is present, the write_user is used.
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

func printSummary(result *projection.ProjectionOutput) {
	title := fmt.Sprintf("Projections for season %d", result.Season)
	if result.AsOfGameweek > 0 {
		title += fmt.Sprintf(" (from gameweek %d)", result.AsOfGameweek)
	}
	fmt.Printf("%s (%d players)\n\n", title, len(result.Players))

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
}
