# Agent Guidelines

## Project Structure

- `projection/` — Core projection engine: `engine.go` (pipeline, scoring rules, in-season blending), `loader.go` (DB queries), `models.go` (types), `db.go` (connection), `fixtures.go` (fixture-difficulty adjustment), `persist.go` (projection-run snapshots), `engine_test.go`
- `draft/` — Draft league state loaders and recommendation logic: `models.go` (types), `load.go` (DB queries), `xi.go` (starting-XI optimiser + marginal swap value), `recommend.go` (candidate generation + waiver simulation), `persist.go` (recommendation logging), `draft_test.go`
- `cmd/project/` — CLI to generate projections (pre-season or in-season) and optionally persist them
- `cmd/backtest/` — CLI for season-level and rolling in-season backtests, plus `-grade-recommendations` to score logged transfer advice
- `cmd/transfers/` — CLI to recommend draft transfers, waiver claims and the starting XI
- `cmd/draft/` — Bubble Tea TUI for live draft management
- `cmd/squad/` — CLI to select the optimal Classic FPL opening squad within budget
- `PERFORMANCE.md` — Backtest results, experiment history, improvement roadmap

## Database

PostgreSQL `mandyville` on `localhost:5432` (user: `postgres`, pass: `password`). Key tables: `players_fixtures`, `fixtures_team_performance`, `fpl_players_gameweeks`, `fpl_season_info`, `players_teams`. English PL `competition_id = 190`. Season convention: the season refers to the starting year of the season for leagues that span multiple calendar years, so 2025 refers to the 2025-2026 season.
- `players_teams` is the single source of truth for player→team mapping (date-ranged `start_date`/`end_date`). All team assignment goes through `LoadPlayerTeams`, never `fpl_season_info` or fixture data.
- FPL Draft game state lives in the `fpl_draft_*` tables (leagues, entries, picks, transactions, ownership, matches, standings, waiver order, entry lineups, sync runs) plus `fpl_player_availability`, and `fpl_season_info.fpl_draft_id` maps players to their draft-game element id. These are written by the `update-fpl-draft` / `update-fpl-availability` crons in the `data` repo; ownership, waiver order and availability are change-only date/time ranges (`end_time IS NULL` marks the open row).
- `fpl_draft_recommendation_runs` / `fpl_draft_recommendations` log what `cmd/transfers` advised (both the free-agent and waiver views) for later grading. They are written by `cmd/transfers` with the write database user.

## Development

- Season backtest: `go run ./cmd/backtest/ -season 2025`
- Rolling in-season backtest: `go run ./cmd/backtest/ -season 2025 -rolling`
- Evaluate with `python3 evaluate.py backtest_2025.json` (not committed — local utility)
- When testing model changes, use isolated branches and compare metrics against the baseline in PERFORMANCE.md
- The TUI requires a TTY and cannot be tested in headless environments
- Output files to the /out directory, create test scripts in the scratch directory

## Model Changes

- Regression coefficients in `engine.go` were trained on 2020-2024 PL data. If retraining, update the comments alongside the constants.
- Competition tiers are defined in `LoadCompetitionTiers` in `loader.go`. Top-7 European leagues are TierTop5.
- Transfer detection relies on `IsTransferIn` set in `computeRates` — players with no PL minutes in the 3 most recent seasons who are now at a PL team.
- Scoring is parameterised by `ScoringRules` (`ClassicRules` vs `DraftRules` in `models.go`). Draft gives GKs 10 points per goal; keep these in sync with `draft.premierleague.com/api/bootstrap-static settings.scoring`.
- In-season blending shrinkage constants live at the top of `engine.go`: `rateShrinkageMinutes` (900, per-90 rates), `minutesShrinkageAppearances` (5, minutes/fixture), `teamShrinkageMatches` (10, team strengths). Tune via the rolling backtest.
- Availability (`availabilityScale` in `engine.go`) scales expected minutes per fixture. A known `news_return` is honoured exactly; without one, `i`/`s` decay back via `1-exp(-n/tau)` over the following fixtures (`injuryReturnTau` 3, `suspensionReturnTau` 1), while `u`/`n` (sold, loaned out, unregistered) stay zero for the season. Never zero a player just because `players_teams` has no PL club for them — see experiment 5 in PERFORMANCE.md.
- The FPL API always leaves `news_return` null; the date is parsed out of the `news` text by the `update-fpl-availability` cron in the `data` repo. Projections are only as good as that parsing, so new news formats need handling there rather than here.
- Projection snapshots are persisted to `fpl_projection_runs`/`fpl_projections` (`cmd/project -persist`). The in-season engine currently recomputes its pre-season prior rather than loading a snapshot — wire the snapshot in before January so mid-season `players_teams` changes don't drift the prior.
- The squad selector (`cmd/squad`) reads the engine's per-gameweek projections from `projections.json` (no longer applies its own `/38` + fixture-multiplier approximation). Classic FPL rules live in `scratch/fantasy-premier-league-rules.md`.
