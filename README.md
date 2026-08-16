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
| `-backtest` | false | Compare against actual FPL points |
| `-db-host` | localhost | Database host |
| `-db-port` | 5432 | Database port |
| `-db-user` | postgres | Database user |
| `-db-pass` | password | Database password |
| `-db-name` | mandyville | Database name |

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
5. **Points conversion** — A 60/40 blend of a manual component model (goals, assists, clean sheets, saves, bonus, cards, DEFCON, goals conceded penalty) and a per-position linear regression trained on historical per-90 stats vs actual FPL points.
6. **VORP** — Projected points minus the replacement-level player at each position (the N+1th ranked player where N = league_size × roster slots).
7. **H2H adjustment** — Penalises inconsistent players: `H2H = projected_points − 2 × stddev(historic_gw_points)`.

See [PERFORMANCE.md](PERFORMANCE.md) for full backtest results and improvement roadmap.
