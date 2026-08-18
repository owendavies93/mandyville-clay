package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	input := flag.String("input", "projections.json", "projection JSON file")
	sessionFlag := flag.String("session", "", "session ID to resume")
	reconcileFlag := flag.String("reconcile", "", "draft result JSON to reconcile against FPL API")
	leagueFlag := flag.Int("league", 0, "FPL Draft league ID (required for -reconcile)")
	applyFlag := flag.Bool("apply", false, "apply corrections from -reconcile to the result file")
	flag.Parse()

	if *reconcileFlag != "" {
		if *leagueFlag == 0 {
			fmt.Fprintf(os.Stderr, "-league is required with -reconcile\n")
			os.Exit(1)
		}
		if err := runReconcile(*reconcileFlag, *leagueFlag, *applyFlag); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	f, err := os.Open(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open %s: %v\n", *input, err)
		os.Exit(1)
	}
	defer f.Close()

	var data ProjectionData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse projections: %v\n", err)
		os.Exit(1)
	}

	m := newModel(data)

	// Resume an existing session when a session ID is provided.
	if *sessionFlag != "" {
		s, err := loadSessionByID(*sessionFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load session %s: %v\n", *sessionFlag, err)
			os.Exit(1)
		}
		if err := m.applySession(s); err != nil {
			fmt.Fprintf(os.Stderr, "failed to resume session %s: %v\n", *sessionFlag, err)
			os.Exit(1)
		}
		m.mode = modeNormal
		m.recalcVORP()
		m.rebuildView()
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Report the new session ID so the draft can be resumed later.
	if fm, ok := final.(model); ok {
		if fm.sessionID != "" {
			fmt.Printf("session saved as: %s\n", fm.sessionID)
			fmt.Printf("resume with: -session %s\n", fm.sessionID)
		}
		if fm.resultPath != "" {
			fmt.Printf("final draft state saved to: %s\n", fm.resultPath)
		}
	}
}
