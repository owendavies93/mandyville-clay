package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Session is the serialisable draft state. Players are referenced by their
// projection PlayerID so the file stays valid across projection refreshes.
type Session struct {
	Season     int         `json:"season"`
	LeagueSize int         `json:"league_size"`
	DraftPos   int         `json:"draft_pos"`
	PickNumber int         `json:"pick_number"`
	MySquad    []int       `json:"my_squad"`  // player IDs drafted by me
	OppTaken   []int       `json:"opp_taken"` // player IDs taken by opponents
	Picks      []draftPick `json:"picks"`     // full pick history with team attribution
	SortMode   int         `json:"sort_mode"`
	Filter     int         `json:"filter"`
	Cursor     int         `json:"cursor"`
}

// sessionDir is the directory where all sessions are stored.
func sessionDir() string {
	return filepath.Join(os.TempDir(), "mandyville-draft")
}

func sessionPath(id string) string {
	return filepath.Join(sessionDir(), id+".json")
}

// newSessionID returns a unique 16-hex-char session identifier.
func newSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; fall back to a timestamp.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// loadSessionByID reads a session file from the session directory.
func loadSessionByID(id string) (*Session, error) {
	raw, err := os.ReadFile(sessionPath(id))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// saveSession writes the current draft state to a new uniquely-named file
// and records the new session ID on the model.
func (m *model) saveSession() error {
	id := newSessionID()

	s := Session{
		Season:     m.data.Season,
		LeagueSize: m.leagueSize,
		DraftPos:   m.draftPos,
		PickNumber: m.pickNumber,
		SortMode:   int(m.sortMode),
		Filter:     int(m.filter),
		Cursor:     m.cursor,
	}

	for _, idx := range m.mySquad {
		s.MySquad = append(s.MySquad, m.data.Players[idx].PlayerID)
	}
	for i, state := range m.playerState {
		if state == TakenByOpponent {
			s.OppTaken = append(s.OppTaken, m.data.Players[i].PlayerID)
		}
	}
	s.Picks = m.picks

	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(sessionDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(sessionPath(id), raw, 0o644); err != nil {
		return err
	}

	m.sessionID = id
	return nil
}

// applySession maps the saved player IDs back onto the current projection
// data and restores draft state.
func (m *model) applySession(s *Session) error {
	if s.Season != m.data.Season {
		return fmt.Errorf("session is for season %d, projections are for season %d", s.Season, m.data.Season)
	}

	// Build player ID -> index lookup.
	idToIdx := make(map[int]int, len(m.data.Players))
	for i, p := range m.data.Players {
		idToIdx[p.PlayerID] = i
	}

	// Reset state.
	m.playerState = make([]DraftState, len(m.data.Players))
	m.mySquad = nil
	m.undoStack = nil // undo history is not persisted across sessions
	m.picks = s.Picks

	m.leagueSize = s.LeagueSize
	m.draftPos = s.DraftPos
	m.pickNumber = s.PickNumber
	m.sortMode = sortMode(s.SortMode)
	m.filter = posFilter(s.Filter)
	m.cursor = s.Cursor

	for _, id := range s.MySquad {
		if idx, ok := idToIdx[id]; ok {
			m.playerState[idx] = DraftedByMe
			m.mySquad = append(m.mySquad, idx)
		}
	}
	for _, id := range s.OppTaken {
		if idx, ok := idToIdx[id]; ok {
			m.playerState[idx] = TakenByOpponent
		}
	}

	return nil
}
