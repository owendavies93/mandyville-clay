# Agent Guidelines

## Project Structure

- `projection/` — Core projection engine: `engine.go` (pipeline, scoring rules, in-season blending), `loader.go` (DB queries), `models.go` (types), `db.go` (connection), `fixtures.go` (fixture-difficulty adjustment), `persist.go` (projection-run snapshots), `engine_test.go`
- `squad/` — Game-agnostic squad primitives shared by the draft and classic games: `Player`, position constants, the 2/5/5/3 shape, the starting-XI optimiser (`BestXI`, `BestXIWithCaptain`), bench ordering and marginal swap value
- `draft/` — Draft league state loaders and recommendation logic: `models.go` (types, re-exporting `squad`), `load.go` (DB queries), `xi.go` (thin wrappers over `squad`), `recommend.go` (candidate generation + waiver simulation), `persist.go` (recommendation logging), `draft_test.go`. Swaps are thresholded and ranked on `Gain`; `H2HGain` and `ROSGain()` are reported for context only. Do not promote `H2HGain` to the decision metric without reworking it: it differences a `points - 2*stddev` floor at player rather than XI level (overstating the variance effect ~3.6x on real squads), its sign is unconditional, and `Consistency` correlates ~+0.56 with projected points, so it systematically prefers weaker, steadier players. Players whose availability is `u`/`n` with zero rest-of-season projection are "dead slots", surfaced regardless of `-min-gain` since a horizon-bound XI gain cannot price a permanently worthless slot.
- `classic/` — Classic entry state loaders and transfer planning: `models.go` (types, tenths-of-£1m money), `load.go` (squad reconstruction, prices, free transfers), `plan.go` (beam-search planner), `persist.go` (recommendation logging), `classic_test.go`
- `cmd/project/` — CLI to generate projections (pre-season or in-season) and optionally persist them
- `cmd/backtest/` — CLI for season-level and rolling in-season backtests, `-grade-recommendations`, and `-classic-sim` (rolling classic season simulator)
- `cmd/transfers/` — CLI to recommend transfers for both games: classic (default) via `-game classic`, draft via `-game draft`
- `cmd/draft/` — Bubble Tea TUI for live draft management
- `cmd/recommendations/` — CLI to list and display past transfer recommendations from both games
- `cmd/squad/` — CLI to select the optimal Classic FPL opening squad within budget
- `PERFORMANCE.md` — Backtest results, experiment history, improvement roadmap

## Database

PostgreSQL `mandyville` on `localhost:5432` (user: `postgres`, pass: `password`). Key tables: `players_fixtures`, `fixtures_team_performance`, `fpl_players_gameweeks`, `fpl_season_info`, `players_teams`. English PL `competition_id = 190`. Season convention: the season refers to the starting year of the season for leagues that span multiple calendar years, so 2025 refers to the 2025-2026 season.
- `players_teams` is the single source of truth for player→team mapping (date-ranged `start_date`/`end_date`). All team assignment goes through `LoadPlayerTeams`, never `fpl_season_info` or fixture data.
- FPL Draft game state lives in the `fpl_draft_*` tables (leagues, entries, picks, transactions, ownership, matches, standings, waiver order, entry lineups, sync runs) plus `fpl_player_availability`, and `fpl_season_info.fpl_draft_id` maps players to their draft-game element id. These are written by the `update-fpl-draft` / `update-fpl-availability` crons in the `data` repo; ownership, waiver order and availability are change-only date/time ranges (`end_time IS NULL` marks the open row).
- `fpl_draft_recommendation_runs` / `fpl_draft_recommendations` log what `cmd/transfers` advised (both the free-agent and waiver views) for later grading. They are written by `cmd/transfers` with the write database user.
- FPL Classic game state lives in the `fpl_classic_*` tables (entries, picks, transfers, entry history, chips), written by the `update-fpl-classic` cron. `fpl_classic_transfers.element_in_cost`/`element_out_cost` are in tenths of £1m, as are `fpl_classic_entry_history.bank`/`value`.
- `fpl_player_prices` is a change-only table (`end_time IS NULL` = current) of classic player prices plus ownership snapshots, written by `update-fpl-info` from the classic bootstrap. Prices are stored in millions (`now_cost / 10`). `cmd/transfers -game classic` reads it, falling back to starting/last-gameweek prices when empty.
- `fpl_classic_recommendation_runs` / `fpl_classic_recommendations` log classic plans (transfers, XI, captain, bench per gameweek) for later grading. Written by `cmd/transfers -game classic` with the write database user.

## Development

- Season backtest: `go run ./cmd/backtest/ -season 2025`
- Rolling in-season backtest: `go run ./cmd/backtest/ -season 2025 -rolling`
- Classic season simulator: `go run ./cmd/backtest/ -classic-sim -season 2025` (caches point-in-time projections under `out/sim-projections/`; `-refresh` to regenerate)
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
- The classic planner (`classic/plan.go`) is a beam search over `(squad, bank, free transfers)` states. Defaults are tuned flags in `cmd/transfers`: horizon 8, beam 200, max 2 transfers/gameweek, pair shortlist 30. The objective is undiscounted XI + captain points minus 4 per paid transfer. The candidate pool is Pareto-filtered by per-gameweek price/points dominance (lossless except in club-limit corners). Bench points and price changes are not modelled; end-of-horizon free transfers are worth nothing (accepted edge effect).
