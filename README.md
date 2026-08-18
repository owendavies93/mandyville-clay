# Mandyville Clay

- Projection Modelling
- TUI for draft selection
- Classic squad selection tooling

## Prerequisites

- Go 1.21+
- PostgreSQL with a populated [mandyville](https://github.com/mandyville) database on `localhost:5432`

## Generate Projections

```
go run ./cmd/project/ -season 2026 -output projections.json
```

Flags:

| Flag | Default | Description |
|---|---|---|
| `-season` | 2026 | Target season |
| `-league-size` | 8 | Number of managers |
| `-output` | projections.json | Output file |
| `-rules` | classic | Scoring rules: `classic` or `draft` |
| `-as-of-gameweek` | 0 | Project from this gameweek onward using in-season data (0 = pre-season) |
| `-persist` | false | Save the run to `fpl_projection_runs`/`fpl_projections` |
| `-config` | — | Path to a mandyville `config.yaml` |
| `-db-*` | — | Database connection overrides |

## Backtests

```
go run ./cmd/backtest/ -season 2025           # pre-season projection vs actuals
go run ./cmd/backtest/ -season 2025 -rolling  # rolling in-season backtest
go run ./cmd/backtest/ -grade-recommendations # score logged transfer advice
```

Flags: `-season` (default 2025), `-rolling`, `-grade-recommendations`, `-league-size`, `-config`, `-db-*`.

## Draft Transfers

```
go run ./cmd/transfers/ -league 12345 -season 2026
```

Recommends same-position transfers, waiver claims and the starting XI for
the upcoming gameweek, from the league state in the `fpl_draft_*` tables
plus fixture-level projections (computed in-process, draft scoring rules).
Every swap is valued by its marginal effect on the optimised starting XI
over the horizon, not the raw player-points delta.

Flags:

| Flag | Default | Description |
|---|---|---|
| `-league` | — | FPL draft league id (required) |
| `-season` | 2026 | Season |
| `-horizon` | 3 | Gameweeks to project ahead |
| `-discount` | 0.9 | Geometric per-gameweek discount |
| `-top` | 10 | Number of candidates to show |
| `-min-gain` | 1.0 | Minimum discounted XI gain to recommend a swap |
| `-json` | — | Write the full candidate set to a JSON file |
| `-input` | — | Reuse a `projections.json` instead of computing in-process |
| `-no-log` | false | Skip writing recommendations to the database |
| `-config`, `-db-*` | — | Database connection (same as `cmd/project`) |

Recommendations are logged to `fpl_draft_recommendation_runs`/
`fpl_draft_recommendations` (unless `-no-log`) so `cmd/backtest
-grade-recommendations` can later score them against actual points.

## Draft TUI

```
go run ./cmd/draft/ -input projections.json
```

## Classic FPL Squad Selector

```
go run ./cmd/squad/ -input projections.json -season 2026 -gameweeks 6
```

Flags:

| Flag | Default | Description |
|---|---|---|
| `-input` | projections.json | Projection JSON file |
| `-budget` | 100.0 | Total budget in millions |
| `-season` | 2026 | Season for prices and fixtures |
| `-gameweeks` | 6 | Number of opening gameweeks to optimise for |
| `-db-*` | — | Database connection (same as `cmd/project`) |

## Projection Model

1. **Per-90 rates** — xG, xA, yellows, reds per 90 minutes, weighted across the 2 prior seasons (70/30). PL-only data preferred when ≥450 PL minutes available in a season.
2. **Bayesian regression** — Player rates are pulled toward positional means using 1800 regression minutes.
3. **Team context** — Attacking rates scaled by `√(team_xG / league_avg_xG)`. Player→team mapping comes from the `players_teams` table (current club for upcoming seasons; team at the summer-window close for backtests).
4. **Minutes projection** — Weighted average of recent seasons with a trend floor at 85% of the most recent season. Transfers from non-PL leagues get competition-tier discounts (Top 7 European leagues: 0.80×, Championship: 0.65×, other: 0.50×) and a minutes floor (1800–2200) when identified as new-to-PL signings.
5. **Fixture-level scoring** — The season total is the sum of per-fixture projections. Each fixture gets opponent difficulty multipliers (attacking vs defensive) and availability-adjusted expected minutes, so blanks, doubles and injuries fall out naturally.
6. **Points conversion** — Per fixture, a 60/40 blend of a manual component model (goals, assists, clean sheets, saves, bonus, cards, DEFCON, goals conceded penalty) and a per-position linear regression trained on historical per-90 stats vs actual FPL points. Scoring is parameterised (`classic` vs `draft` rules — draft gives GKs 10 points per goal).
7. **In-season updating** — `-as-of-gameweek N` blends current-season form into the pre-season prior with sample-size shrinkage (per-90 rates shrink with current-season minutes, team strengths with matches played), and applies the current injury/suspension status.
8. **VORP** — Projected points minus the replacement-level player at each position (the N+1th ranked player where N = league_size × roster slots).
9. **H2H adjustment** — Penalises inconsistent players: `H2H = projected_points − 2 × stddev(historic_gw_points)`.

See [PERFORMANCE.md](PERFORMANCE.md) for full backtest results and improvement roadmap.
