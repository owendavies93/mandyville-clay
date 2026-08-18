package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mandyville/mandyville-draft/draft"
)

const fplDraftBootstrapURL = "https://draft.premierleague.com/api/bootstrap-static"

// draftBootstrap is the subset of the FPL Draft bootstrap-static payload
// needed to identify elements that have no players-table match.
type draftBootstrap struct {
	Elements []struct {
		ID          int    `json:"id"`
		Code        int    `json:"code"`
		FirstName   string `json:"first_name"`
		SecondName  string `json:"second_name"`
		ElementType int    `json:"element_type"`
		Team        int    `json:"team"`
	} `json:"elements"`
	ElementTypes []struct {
		ID        int    `json:"id"`
		ShortName string `json:"singular_name_short"`
	} `json:"element_types"`
	Teams []struct {
		ID        int    `json:"id"`
		ShortName string `json:"short_name"`
	} `json:"teams"`
}

// elementIdentity is the debug identity of a draft element.
type elementIdentity struct {
	code  int
	label string // "First Last (POS, TEAM)"
}

// resolve maps every element id to its FPL code and a readable label.
func (b *draftBootstrap) resolve() map[int]elementIdentity {
	pos := map[int]string{}
	for _, t := range b.ElementTypes {
		switch t.ShortName {
		case "GKP":
			pos[t.ID] = draft.PosGK
		case "DEF":
			pos[t.ID] = draft.PosDEF
		case "MID":
			pos[t.ID] = draft.PosMID
		case "FWD":
			pos[t.ID] = draft.PosFWD
		default:
			pos[t.ID] = t.ShortName
		}
	}
	team := map[int]string{}
	for _, t := range b.Teams {
		team[t.ID] = t.ShortName
	}

	out := make(map[int]elementIdentity, len(b.Elements))
	for _, e := range b.Elements {
		name := strings.TrimSpace(e.FirstName + " " + e.SecondName)
		if name == "" {
			name = fmt.Sprintf("element %d", e.ID)
		}
		out[e.ID] = elementIdentity{
			code:  e.Code,
			label: fmt.Sprintf("%s (%s, %s)", name, pos[e.ElementType], team[e.Team]),
		}
	}
	return out
}

// fetchDraftBootstrap returns the live draft bootstrap payload, used only
// for the "cannot evaluate" debug section. Failures are surfaced as errors
// and the caller degrades gracefully.
func fetchDraftBootstrap() (*draftBootstrap, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fplDraftBootstrapURL)
	if err != nil {
		return nil, fmt.Errorf("draft API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("draft API returned %d", resp.StatusCode)
	}
	var b draftBootstrap
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return nil, fmt.Errorf("decoding draft bootstrap: %w", err)
	}
	return &b, nil
}

// playerRef is a players row looked up by fpl_id (the draft element code).
type playerRef struct {
	id    int
	code  int
	first string
	last  string
}

// playersByCode returns players rows keyed by fpl_id for the given codes.
func playersByCode(db *sql.DB, codes []int) (map[int]playerRef, error) {
	out := make(map[int]playerRef, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := db.Query(
		`SELECT id, fpl_id, first_name, last_name FROM players WHERE fpl_id = ANY($1)`,
		codes,
	)
	if err != nil {
		return nil, fmt.Errorf("looking up players by code: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p playerRef
		if err := rows.Scan(&p.id, &p.code, &p.first, &p.last); err != nil {
			return nil, err
		}
		out[p.code] = p
	}
	return out, rows.Err()
}

// playersByID returns player display names keyed by id, for players who
// have no projection (so their name is not in the projection pool).
func playersByID(db *sql.DB, ids []int) (map[int]string, error) {
	out := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(
		`SELECT id, first_name, last_name FROM players WHERE id = ANY($1)`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("looking up players by id: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var first, last string
		if err := rows.Scan(&id, &first, &last); err != nil {
			return nil, err
		}
		out[id] = strings.TrimSpace(first + " " + last)
	}
	return out, rows.Err()
}

// printUnmatched explains the players and elements the recommender cannot
// evaluate, so a useful free agent is never silently invisible. For
// unmatched elements it fetches the live draft bootstrap (best effort) to
// show who each element is, then diagnoses why no players row matched.
func printUnmatched(db *sql.DB, season int, unmatchedFree, myUnmatched, unprojectedFree []draft.Ownership) {
	if len(unmatchedFree) == 0 && len(myUnmatched) == 0 && len(unprojectedFree) == 0 {
		return
	}

	var (
		ident   map[int]elementIdentity
		byCode  map[int]playerRef
		codeErr error // set if the players-by-code lookup itself failed
	)
	if len(unmatchedFree) > 0 || len(myUnmatched) > 0 {
		if boot, err := fetchDraftBootstrap(); err == nil {
			ident = boot.resolve()
			codes := make([]int, 0, len(unmatchedFree)+len(myUnmatched))
			for _, list := range [][]draft.Ownership{unmatchedFree, myUnmatched} {
				for _, o := range list {
					if id, ok := ident[o.Element]; ok {
						codes = append(codes, id.code)
					}
				}
			}
			byCode, codeErr = playersByCode(db, codes)
		}
	}

	fmt.Println("Cannot evaluate (check manually):")

	if len(myUnmatched) > 0 {
		fmt.Printf("  You own %d unmatched element(s):\n", len(myUnmatched))
		printElements(db, season, myUnmatched, ident, byCode, codeErr)
	}

	if len(unmatchedFree) > 0 {
		fmt.Printf("  %d free agent(s) unmatched to a player:\n", len(unmatchedFree))
		printElements(db, season, unmatchedFree, ident, byCode, codeErr)
	}

	if len(unprojectedFree) > 0 {
		names, _ := playersByID(db, playerIDs(unprojectedFree))
		fmt.Printf("  %d free agent(s) have no projection (missing fpl_season_info or fixtures?):\n", len(unprojectedFree))
		for _, o := range unprojectedFree {
			name := names[o.PlayerID]
			if name == "" {
				if id, ok := ident[o.Element]; ok {
					name = id.label
				}
			}
			if name == "" {
				name = "unknown"
			}
			fmt.Printf("    player %-6d  element %d  %s\n", o.PlayerID, o.Element, name)
		}
	}
	fmt.Println()
}

// playerIDs collects the player ids from a set of ownership rows.
func playerIDs(list []draft.Ownership) []int {
	ids := make([]int, 0, len(list))
	for _, o := range list {
		ids = append(ids, o.PlayerID)
	}
	return ids
}

// printElements prints one entry per unmatched element: its bootstrap
// identity, the stored availability, and the reason no players row matched.
func printElements(db *sql.DB, season int, list []draft.Ownership, ident map[int]elementIdentity, byCode map[int]playerRef, lookupErr error) {
	elems := make([]int, 0, len(list))
	for _, o := range list {
		elems = append(elems, o.Element)
	}
	sort.Ints(elems)

	avail, err := draft.LoadElementAvailability(db, season, elems)
	if err != nil {
		avail = map[int]draft.ElementAvailability{}
	}

	for _, elem := range elems {
		a := avail[elem]
		availStr := ""
		if a.Status != "" {
			availStr += fmt.Sprintf("  status %s", a.Status)
		}
		if a.DraftRank > 0 {
			availStr += fmt.Sprintf("  rank %d", a.DraftRank)
		}
		if a.News != "" {
			availStr += fmt.Sprintf("  news %q", a.News)
		}

		if id, ok := ident[elem]; ok {
			fmt.Printf("    element %-3d  code %-7d  %s%s\n", elem, id.code, id.label, availStr)
			switch {
			case lookupErr != nil:
				fmt.Printf("        → could not check players.fpl_id %d: %v\n", id.code, lookupErr)
			case byCode[id.code].id != 0:
				p := byCode[id.code]
				fmt.Printf("        → players.fpl_id %d is players.id %d (%s %s); ownership.player_id is NULL — re-run update-fpl-draft\n",
					id.code, p.id, p.first, p.last)
			default:
				fmt.Printf("        → players.fpl_id %d not in players (new signing? awaiting classic sync)\n", id.code)
			}
		} else {
			fmt.Printf("    element %d%s\n", elem, availStr)
		}
	}
}
