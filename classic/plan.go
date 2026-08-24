package classic

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mandyville/mandyville-draft/squad"
)

// Move is a single transfer: sell Out for In. Both are same-position.
type Move struct {
	OutElement int
	InElement  int
	Out        *Member
	In         *PoolPlayer
	Position   string
	Proceeds   int     // selling price received, tenths
	Cost       int     // purchase price paid, tenths
	Rank       float64 // horizon-points-delta proxy, used to shortlist pairs
	Gain       float64 // exact XI+captain gain (set by ThisWeekSingles)
}

// Step is one gameweek of a plan.
type Step struct {
	GW        int
	Moves     []Move
	FreeUsed  int     // free transfers consumed
	Hits      int     // paid transfers
	XI        []int   // starting player ids
	Bench     []int   // bench player ids (in recommended order)
	Captain     int // player id
	ViceCaptain int // player id
	Points    float64 // projected XI + captain points (gross)
	NetPoints float64 // gross minus hit costs
}

// Plan is a transfer plan over the horizon.
type Plan struct {
	Total     float64 // sum of net points over the horizon
	Steps     []Step
	Immediate []Move // this week's transfers (empty = roll)
	PureRoll  bool   // no transfers in any gameweek
}

// Outcome is the planner's decision: the recommended plan, the roll
// baselines and the leading alternatives.
type Outcome struct {
	Recommended  *Plan
	RollThisWeek *Plan // best plan that makes no transfer this week
	PureRoll     *Plan // never transfer at all
	Alternatives []*Plan
}

// Planner holds the search parameters and precomputed candidate pools.
type Planner struct {
	pool          *Pool
	startGW       int
	horizon       int
	beamWidth     int
	maxTransfers  int
	pairShortlist int
	candidates    map[string][]*PoolPlayer // position -> pareto frontier
	horizonPts    map[int]float64          // element -> sum of points over horizon

	// Progress, if non-nil, is called after each gameweek of the search
	// with the gameweek, beam size, best-so-far value and elapsed time.
	Progress func(gw, states int, best float64, elapsed time.Duration)
}

// NewPlanner builds a planner with pareto-filtered candidate pools. The
// candidate pool per position is reduced to its price/points frontier:
// players that are both more expensive and lower-projected than another
// available player are dropped. This is lossless except in rare club-limit
// corners.
func NewPlanner(pool *Pool, startGW, horizon, beamWidth, maxTransfers, pairShortlist int) *Planner {
	p := &Planner{
		pool:          pool,
		startGW:       startGW,
		horizon:       horizon,
		beamWidth:     beamWidth,
		maxTransfers:  maxTransfers,
		pairShortlist: pairShortlist,
		candidates:    map[string][]*PoolPlayer{},
		horizonPts:    map[int]float64{},
	}

	for _, pp := range pool.ByElement {
		p.horizonPts[pp.Element] = horizonPoints(pp.Player, startGW, horizon)
	}

	byPos := map[string][]*PoolPlayer{}
	for _, pp := range pool.ByElement {
		if pp.Player == nil {
			continue
		}
		byPos[pp.Player.Position] = append(byPos[pp.Player.Position], pp)
	}
	for pos, cands := range byPos {
		p.candidates[pos] = paretoFrontier(cands, startGW, horizon)
	}
	return p
}

// horizonPoints sums a player's projected points over the horizon.
func horizonPoints(p *squad.Player, startGW, horizon int) float64 {
	if p == nil {
		return 0
	}
	total := 0.0
	for i := 0; i < horizon; i++ {
		total += p.PointsIn(startGW + i)
	}
	return total
}

// dominates reports whether candidate a dominates b: a is no more expensive
// and scores at least as many points as b in every gameweek of the horizon,
// with a strict improvement somewhere. Dropping dominated players is lossless
// for transfer purposes (the only caveat is club-limit corners).
func dominates(a, b *PoolPlayer, startGW, horizon int) bool {
	if a.Price > b.Price {
		return false
	}
	strict := a.Price < b.Price
	for i := 0; i < horizon; i++ {
		gw := startGW + i
		ap := a.Player.PointsIn(gw)
		bp := b.Player.PointsIn(gw)
		if ap < bp {
			return false
		}
		if ap > bp {
			strict = true
		}
	}
	return strict
}

// paretoFrontier drops candidates dominated by another candidate. The result
// preserves every player that is strictly best in at least one gameweek or
// uniquely cheap, so no genuinely useful option is removed.
func paretoFrontier(cands []*PoolPlayer, startGW, horizon int) []*PoolPlayer {
	out := make([]*PoolPlayer, 0, len(cands))
	for _, c := range cands {
		dominated := false
		for _, d := range out {
			if dominates(d, c, startGW, horizon) {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, c)
		}
	}
	return out
}

// state is a beam-search node: a squad plus its accumulated value.
type state struct {
	roster       map[int]*Member // element -> member
	bank         int             // tenths
	ft           int             // free transfers available this gameweek
	pts          float64         // accumulated net points through the last scored gameweek
	steps        []Step
	immediateKey string // canonical key of this week's moves ("" = roll)
	pureRoll     bool   // no transfers made in any gameweek so far
}

// playersMap returns the projected roster keyed by player id, for the XI
// optimiser.
func (s *state) playersMap() map[int]*squad.Player {
	out := make(map[int]*squad.Player, len(s.roster))
	for _, m := range s.roster {
		if m.Player != nil {
			out[m.Player.ID] = m.Player
		}
	}
	return out
}

// key returns a dedupe key for the state: the roster, bank and free
// transfers (two states with the same key have identical futures, so only
// the higher-scoring one is worth keeping).
func (s *state) key() string {
	elems := make([]int, 0, len(s.roster))
	for e := range s.roster {
		elems = append(elems, e)
	}
	sort.Ints(elems)
	var b strings.Builder
	for _, e := range elems {
		b.WriteString(strconv.Itoa(e))
		b.WriteByte(',')
	}
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(s.bank))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(s.ft))
	return b.String()
}

// actionKey canonicalises a set of moves for grouping by this-week action.
func actionKey(moves []Move) string {
	if len(moves) == 0 {
		return ""
	}
	parts := make([]string, len(moves))
	for i, m := range moves {
		parts[i] = fmt.Sprintf("%d>%d", m.OutElement, m.InElement)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// cloneRoster returns a shallow copy of the roster (members are immutable).
func cloneRoster(r map[int]*Member) map[int]*Member {
	out := make(map[int]*Member, len(r))
	for e, m := range r {
		out[e] = m
	}
	return out
}

// applyMoves returns a new state with the moves applied and the bank and
// free transfers updated. Hit accounting is done by the caller.
func (s *state) applyMoves(moves []Move) *state {
	ns := &state{
		roster:       cloneRoster(s.roster),
		bank:         s.bank,
		ft:           s.ft,
		pts:          s.pts,
		steps:        append([]Step(nil), s.steps...),
		immediateKey: s.immediateKey,
		pureRoll:     s.pureRoll && len(moves) == 0,
	}
	for _, m := range moves {
		delete(ns.roster, m.OutElement)
		ns.roster[m.InElement] = &Member{
			Element:       m.InElement,
			PlayerID:      m.In.PlayerID,
			Player:        m.In.Player,
			Position:      m.Position,
			Name:          m.In.Player.Name,
			TeamID:        m.In.TeamID,
			PurchasePrice: m.In.Price,
			CurrentPrice:  m.In.Price,
		}
		ns.bank += m.Proceeds - m.Cost
	}
	return ns
}

// generateSingles returns every feasible single transfer for the state,
// ranked by a cheap horizon-points-delta proxy so the pair shortlist can
// pick promising pairs without a full XI evaluation per move.
func (p *Planner) generateSingles(s *state) []Move {
	var moves []Move
	for elem, m := range s.roster {
		cands := p.candidates[m.Position]
		for _, c := range cands {
			if _, owned := s.roster[c.Element]; owned {
				continue
			}
			proceeds := SellingPrice(m.PurchasePrice, m.CurrentPrice)
			if s.bank+proceeds-c.Price < 0 {
				continue
			}
			if !clubOK(s.roster, elem, c) {
				continue
			}
			moves = append(moves, Move{
				OutElement: elem, InElement: c.Element, Out: m, In: c,
				Position: m.Position, Proceeds: proceeds, Cost: c.Price,
				Rank: p.horizonPts[c.Element] - p.horizonPts[elem],
			})
		}
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].Rank > moves[j].Rank })
	return moves
}

// clubOK reports whether selling element `out` and buying candidate `c`
// keeps every club at three or fewer players. Unmatched members (team 0)
// are not counted.
func clubOK(roster map[int]*Member, out int, c *PoolPlayer) bool {
	counts := map[int]int{}
	for e, m := range roster {
		if e == out || m.TeamID == 0 {
			continue
		}
		counts[m.TeamID]++
	}
	counts[c.TeamID]++
	return counts[c.TeamID] <= 3
}

// generatePairs combines the top shortlist of single moves into two-transfer
// sets with distinct outs and ins.
func (p *Planner) generatePairs(singles []Move, s *state) [][]Move {
	var sets [][]Move
	n := len(singles)
	if n > p.pairShortlist {
		n = p.pairShortlist
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			a, b := singles[i], singles[j]
			if a.OutElement == b.OutElement || a.InElement == b.InElement {
				continue
			}
			// Budget: apply both and check the bank stays non-negative.
			if s.bank+a.Proceeds-a.Cost+b.Proceeds-b.Cost < 0 {
				continue
			}
			// Club limit: verify the pair jointly stays within 3-per-club.
			if !pairClubOK(s.roster, a, b) {
				continue
			}
			sets = append(sets, []Move{a, b})
		}
	}
	return sets
}

// pairClubOK checks the 3-per-club constraint after applying both moves
// simultaneously (two buys from the same club could individually pass but
// jointly exceed the limit).
func pairClubOK(roster map[int]*Member, a, b Move) bool {
	counts := map[int]int{}
	for e, m := range roster {
		if m.TeamID == 0 || e == a.OutElement || e == b.OutElement {
			continue
		}
		counts[m.TeamID]++
	}
	counts[a.In.TeamID]++
	counts[b.In.TeamID]++
	return counts[a.In.TeamID] <= 3 && counts[b.In.TeamID] <= 3
}

// expand generates the next-generation states from s for gameweek gw.
func (p *Planner) expand(s *state, gw int) []*state {
	var out []*state

	// Roll (no transfer this week).
	out = append(out, p.transition(s, nil, gw))

	if p.maxTransfers >= 1 {
		singles := p.generateSingles(s)
		for _, mv := range singles {
			out = append(out, p.transition(s, []Move{mv}, gw))
		}
		if p.maxTransfers >= 2 {
			for _, pair := range p.generatePairs(singles, s) {
				out = append(out, p.transition(s, pair, gw))
			}
		}
	}
	return out
}

// transition applies the moves for gameweek gw, scores it and advances the
// free-transfer clock.
func (p *Planner) transition(s *state, moves []Move, gw int) *state {
	ns := s.applyMoves(moves)

	freeUsed := len(moves)
	if freeUsed > ns.ft {
		freeUsed = ns.ft
	}
	hits := len(moves) - freeUsed

	pm := ns.playersMap()
	xi, captain, gross := squad.BestXIWithCaptain(pm, gw)
	vice := viceCaptain(pm, xi, captain, gw)
	bench := squad.BenchOrder(pm, xi, gw)
	net := gross - 4*float64(hits)

	ns.ft -= freeUsed
	ns.pts += net
	step := Step{
		GW: gw, Moves: moves, FreeUsed: freeUsed, Hits: hits,
		XI: xi, Bench: bench, Captain: captain, ViceCaptain: vice,
		Points: gross, NetPoints: net,
	}
	ns.steps = append(ns.steps, step)

	if gw == p.startGW {
		ns.immediateKey = actionKey(moves)
	}
	// Earn one free transfer for completing the gameweek.
	ns.ft++
	if ns.ft > 5 {
		ns.ft = 5
	}
	return ns
}

// prune dedupes states (roster+bank+ft), keeps the top beamWidth by points,
// and force-keeps the pure-roll lineage and the best state per immediate
// action so the roll baseline and alternatives stay exact.
func prune(states []*state, beamWidth int) []*state {
	dedup := map[string]*state{}
	for _, s := range states {
		k := s.key()
		if cur, ok := dedup[k]; !ok || s.pts > cur.pts {
			dedup[k] = s
		}
	}

	all := make([]*state, 0, len(dedup))
	for _, s := range dedup {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].pts > all[j].pts })

	keep := map[*state]bool{}
	var kept []*state

	// Best state per immediate action.
	bestByAction := map[string]*state{}
	for _, s := range all {
		if cur, ok := bestByAction[s.immediateKey]; !ok || s.pts > cur.pts {
			bestByAction[s.immediateKey] = s
		}
	}

	// Force-keep the pure-roll lineage and one state per immediate action.
	for _, s := range all {
		if s.pureRoll && !keep[s] {
			keep[s] = true
			kept = append(kept, s)
			break
		}
	}

	// Deterministically pick the best state per action, limited to an extra
	// beamWidth slots so the beam never exceeds 2*beamWidth.
	byAction := make([]*state, 0, len(bestByAction))
	for _, s := range bestByAction {
		byAction = append(byAction, s)
	}
	sort.Slice(byAction, func(i, j int) bool { return byAction[i].pts > byAction[j].pts })
	for _, s := range byAction {
		if len(kept) >= 2*beamWidth {
			break
		}
		if !keep[s] {
			keep[s] = true
			kept = append(kept, s)
		}
	}

	// Fill to beamWidth with the highest-scoring remaining states.
	for _, s := range all {
		if len(kept) >= beamWidth {
			break
		}
		if !keep[s] {
			keep[s] = true
			kept = append(kept, s)
		}
	}
	return kept
}

// Plan runs the beam search and returns the recommended plan, baselines and
// alternatives. An error is returned only if the squad has no projectable
// players at all.
func (p *Planner) Plan(squad *Squad) (*Outcome, error) {
	initial := &state{
		roster:   cloneRoster(squad.Members),
		bank:     squad.Bank,
		ft:       squad.FreeTransfers,
		pureRoll: true,
	}

	if len(initial.playersMap()) == 0 {
		return nil, fmt.Errorf("no projectable players in squad")
	}

	beam := []*state{initial}
	start := time.Now()
	for gw := p.startGW; gw < p.startGW+p.horizon; gw++ {
		var next []*state
		for _, s := range beam {
			next = append(next, p.expand(s, gw)...)
		}
		beam = prune(next, p.beamWidth)
		if p.Progress != nil {
			best := beam[0].pts
			for _, s := range beam[1:] {
				if s.pts > best {
					best = s.pts
				}
			}
			p.Progress(gw, len(beam), best, time.Since(start))
		}
	}

	best := beam[0]
	for _, s := range beam[1:] {
		if s.pts > best.pts {
			best = s
		}
	}

	// Baselines and alternatives from the final generation, grouped by
	// immediate action.
	byAction := map[string]*state{}
	for _, s := range beam {
		if cur, ok := byAction[s.immediateKey]; !ok || s.pts > cur.pts {
			byAction[s.immediateKey] = s
		}
	}

	out := &Outcome{
		Recommended:  toPlan(best),
		RollThisWeek: toPlan(byAction[""]),
	}
	for _, s := range beam {
		if s.pureRoll {
			out.PureRoll = toPlan(s)
			break
		}
	}

	// Alternatives: the best plans whose immediate action differs from the
	// recommended one (and from rolling), up to three.
	var altKeys []string
	for k := range byAction {
		if k == "" || k == best.immediateKey {
			continue
		}
		altKeys = append(altKeys, k)
	}
	sort.Slice(altKeys, func(i, j int) bool {
		return byAction[altKeys[i]].pts > byAction[altKeys[j]].pts
	})
	for _, k := range altKeys {
		if len(out.Alternatives) >= 3 {
			break
		}
		out.Alternatives = append(out.Alternatives, toPlan(byAction[k]))
	}

	return out, nil
}

// toPlan converts a terminal state into a Plan.
func toPlan(s *state) *Plan {
	if s == nil {
		return nil
	}
	pl := &Plan{Total: s.pts, Steps: s.steps, PureRoll: s.pureRoll}
	if len(s.steps) > 0 {
		pl.Immediate = s.steps[0].Moves
	}
	return pl
}

// ThisWeekSingles ranks every feasible single transfer for the upcoming
// gameweek by its exact marginal XI+captain gain, highest first. This backs
// the JSON output's candidate list so near-misses can be eyeballed without
// rerunning the search.
func (p *Planner) ThisWeekSingles(sq *Squad) []Move {
	initial := &state{
		roster: cloneRoster(sq.Members),
		bank:   sq.Bank,
		ft:     sq.FreeTransfers,
	}
	_, _, base := squad.BestXIWithCaptain(initial.playersMap(), p.startGW)

	moves := p.generateSingles(initial)
	for i := range moves {
		ns := initial.applyMoves([]Move{moves[i]})
		_, _, total := squad.BestXIWithCaptain(ns.playersMap(), p.startGW)
		moves[i].Gain = total - base
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].Gain > moves[j].Gain })
	return moves
}

// viceCaptain returns the highest-projected XI player after the captain.
func viceCaptain(players map[int]*squad.Player, xi []int, captain, gw int) int {
	vice := 0
	best := -1.0
	for _, id := range xi {
		if id == captain {
			continue
		}
		if p := players[id].PointsIn(gw); p > best {
			best = p
			vice = id
		}
	}
	if vice == 0 {
		vice = captain
	}
	return vice
}
