# Agent Guidelines

## Project Structure

- `projection/` — Core projection engine: `engine.go` (pipeline + scoring), `loader.go` (DB queries), `models.go` (types), `db.go` (connection), `fixtures.go` (fixture-difficulty adjustment)
- `cmd/project/` — CLI to generate projections and run backtests
- `cmd/draft/` — Bubble Tea TUI for live draft management
- `cmd/squad/` — CLI to select the optimal Classic FPL opening squad within budget
- `PERFORMANCE.md` — Backtest results, experiment history, improvement roadmap

## Database

PostgreSQL `mandyville` on `localhost:5432` (user: `postgres`, pass: `password`). Key tables: `players_fixtures`, `fixtures_team_performance`, `fpl_players_gameweeks`, `fpl_season_info`, `players_teams`. English PL `competition_id = 190`. Season convention: `season=2025` means 2024-25.
- `players_teams` is the single source of truth for player→team mapping (date-ranged `start_date`/`end_date`). All team assignment goes through `LoadPlayerTeams`, never `fpl_season_info` or fixture data.

## Development

- Backtest with `go run ./cmd/project/ -season 2025 -backtest -output backtest_2025.json`
- Evaluate with `python3 evaluate.py backtest_2025.json` (not committed — local utility)
- When testing model changes, use isolated branches and compare metrics against the baseline in PERFORMANCE.md
- The TUI requires a TTY and cannot be tested in headless environments
- Output files to the /out directory, create test scripts in the scratch directory

## Model Changes

- Regression coefficients in `engine.go` were trained on 2020-2024 PL data. If retraining, update the comments alongside the constants.
- Competition tiers are defined in `LoadCompetitionTiers` in `loader.go`. Top-7 European leagues are TierTop5.
- Transfer detection relies on `IsTransferIn` set in `computeRates` — players with no PL minutes in the 3 most recent seasons who are now at a PL team.
- The squad selector (`cmd/squad`) uses `LoadPlayerPrices` (starting prices, GW1 fallback) and `LoadFixturesByGameweek` (via the `fixtures_fpl_gameweeks` mapping table) plus `FixtureDifficultyMultiplier` in `fixtures.go`. Classic FPL rules live in `scratch/fantasy-premier-league-rules.md`.
