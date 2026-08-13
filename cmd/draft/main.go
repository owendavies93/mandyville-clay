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
	flag.Parse()

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
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
