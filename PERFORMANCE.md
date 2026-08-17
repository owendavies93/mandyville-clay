# Projection Model Performance

## Backtest: 2025 Season

Projections for 803 FPL players using data from seasons prior to 2025. Compared against actual 2025 FPL points.

### Overall Metrics

| Metric | Value |
|---|---|
| RMSE (all 803) | 46.3 |
| MAE (all 803) | 32.9 |
| Spearman rank correlation (players >30 pts, n=360) | 0.351 |
| Top-120 overlap (draft pool capture) | 59/120 (49%) |
| Top-60 overlap (early picks) | 22/60 (37%) |

### Positional Breakdown (players >30 actual pts)

| Position | Top-10 Overlap | Mean Error | MAE |
|---|---|---|---|
| GK | 6/10 | +5.6 | 39.7 |
| DEF | 3/10 | +6.0 | 37.4 |
| MID | 3/10 | +8.7 | 38.4 |
| FWD | 4/10 | −6.5 | 50.4 |

### How the top-120 actual scorers break down

| Category | Count | Description |
|---|---|---|
| Correctly in our top 120 | 59 | Model ranked them in the top 120 |
| Close miss | 33 | Moderate under-projection, just outside top 120 |
| Breakout | 15 | Career-best season, >50 pts above projection |
| Transfer in | 13 | Non-PL history, projected <50 pts |

### Notable projections vs actuals

| Player | Projected | Actual | Diff |
|---|---|---|---|
| Haaland | 213 | 232 | −19 |
| Watkins | 170 | 167 | +3 |
| Rice | 151 | 173 | −22 |
| Van Dijk | 182 | 171 | +11 |
| Szoboszlai | 156 | 160 | −4 |
| Raya | 155 | 161 | −6 |
| Tarkowski | 156 | 170 | −15 |
| Anderson | 139 | 180 | −42 |
| Salah | 295 | 123 | +172 |
| Igor Thiago | 9 | 181 | −172 |

### Known error sources

- **Players who left the PL** are projected based on historical PL data but scored 0 (Ederson, Onana, Gündogan, Kulusevski). These are the biggest over-projections.
- **Salah** had an anomalous decline season (0.37 xG/90 vs career 0.65+). The model can't predict age-related or form-related decline without birth date data.
- **New-to-PL transfers** with no PL fixture history (Igor Thiago, Mukiele, Roefs) are projected very low because the model has no way to know they'll become starters.

## Experiment Results

Four high-impact changes were tested on isolated branches against the 2025 backtest. Each branch starts from the baseline model.

### Baseline (current model)

| Metric | Value |
|---|---|
| Spearman (>30pts) | 0.351 |
| MAE | 39.5 |
| RMSE | 50.3 |
| Mean error | +5.8 |
| Top-60 / Top-120 | 22 / 59 |
| GK top-10 / bias / MAE | 6 / +5.6 / 39.7 |
| DEF top-10 / bias / MAE | 3 / +6.0 / 37.4 |
| MID top-10 / bias / MAE | 3 / +8.7 / 38.4 |
| FWD top-10 / bias / MAE | 4 / −6.5 / 50.4 |

### 1. Position-specific minutes model (`experiment/position-minutes`)

Replaced the crude avg-min-per-match fullMatchFrac estimate with position-specific 60+ minute rates calibrated from PL data (DEF 80%, MID 64%). Scales per-player based on their projected avg minutes vs position norm. Applied to MID and DEF only — GK and FWD keep the original logic since the change worsened FWD bias.

| Metric | Baseline | All positions | MID/DEF only |
|---|---|---|---|
| Spearman | 0.351 | 0.352 | **0.352** |
| MAE | 39.5 | 38.3 | **38.4** |
| Mean error | +5.8 | +1.7 | **+2.4** |
| Top-60 / Top-120 | 22/59 | 22/59 | 21/59 |
| GK bias | +5.6 | +4.0 | **+5.6** (unchanged) |
| DEF bias | +6.0 | +2.9 | **+2.9** |
| MID bias | +8.7 | +3.8 | **+3.8** |
| FWD bias | −6.5 | −12.3 | **−6.5** (unchanged) |

**Verdict**: Good bias correction for DEF/MID without hurting FWD. MAE nearly as good as the all-positions variant (38.4 vs 38.3). The Top-60 dip of 1 is noise from borderline ranking shifts. MID/DEF-only is the better version.

### 2. FPL regression model (`experiment/fpl-regression`)

Trained per-position linear regressions from historical per-90 stats (xG, xA, yellows, CS rate, goals conceded) to actual FPL points per 90 using 2020-2024 data. Blended 60% manual model + 40% regression.

| Metric | Baseline | After | Δ |
|---|---|---|---|
| Spearman | 0.351 | **0.352** | +0.001 |
| MAE | 39.5 | **37.1** | −2.4 |
| Mean error | +5.8 | **−3.6** | −9.4 |
| Top-60 / Top-120 | 22/59 | 22/60 | +1 |
| GK bias | +5.6 | **−1.4** | −7.0 |
| DEF bias | +6.0 | **−6.3** | −12.3 |
| MID bias | +8.7 | **+0.1** | −8.6 |
| FWD bias | −6.5 | −10.9 | −4.4 |

**Verdict**: Best MAE improvement (−2.4). The regression pulls all positions closer to zero bias, especially MID (+0.1 is near-perfect). DEF swings to slight under-projection. The learned bonus/DEFCON relationships are more accurate than the manual calibrations.

### 3. Transfer detection (`experiment/transfer-detection`)

Detects players new to PL (no PL minutes in last 3 seasons) who are now at PL clubs. Gives them a minutes floor (1800-2200 based on historical minutes). Also added Primeira Liga and Eredivisie to TierTop5 (0.80× instead of 0.50× minutes discount).

| Metric | Baseline | After | Δ |
|---|---|---|---|
| Spearman | 0.351 | **0.352** | +0.001 |
| MAE | 39.5 | 38.9 | −0.6 |
| Mean error | +5.8 | +9.0 | +3.2 |
| Top-60 / Top-120 | 22/59 | 22/59 | — |
| FWD bias | −6.5 | **−2.2** | +4.3 |

Key individual improvements:

| Player | Before | After | Actual |
|---|---|---|---|
| Gyökeres | 19.7 | **90.0** | 127 |
| Zubimendi | 116.9 | **130.7** | 129 |
| Cherki | 121.1 | **127.6** | 128 |
| Šeško | 107.5 | **113.6** | 111 |
| Le Fée | 48.9 | **86.6** | 147 |
| Mukiele | 40.5 | **96.4** | 151 |

**Verdict**: Dramatically improves projections for PL transfers from foreign leagues. FWD bias nearly eliminated. Mean error increased because some boosted transfer players didn't end up playing, but the transfers who DO play are now much better projected. Essential for draft accuracy since foreign transfers are often high-value picks.

### 4. Rate caps at 95th percentile (`experiment/rate-caps`)

Caps regressed xG/90 and xA/90 at the 95th percentile for each position. Prevents runaway projections for extreme-rate players like Salah.

| Metric | Baseline | After | Δ |
|---|---|---|---|
| Spearman | 0.351 | 0.349 | −0.002 |
| MAE | 39.5 | 39.3 | −0.2 |
| Mean error | +5.8 | +5.4 | −0.4 |
| Top-60 / Top-120 | 22/59 | 22/59 | — |

Key impact: Salah 295→230 (actual 123), Haaland 213→198 (actual 232).

**Verdict**: Marginal overall. Helps with extreme outlier calibration (Salah is much more reasonable) but hurts legitimate elite projections (Haaland is now under-projected by 34 instead of 19). The 95th percentile hard cap is too aggressive for genuinely elite players.

### Recommendations

1. **Include**: Transfer detection + Primeira Liga/Eredivisie tier fix. Essential for correctly projecting foreign transfers, which are high-value draft picks.
2. **Include**: Position-specific minutes model (MID/DEF only). Corrects appearance point over-estimation for midfielders and defenders without worsening the forward under-projection.
3. **Include**: FPL regression blend. Best overall MAE improvement and near-perfect MID bias correction.
4. **Exclude**: Rate caps at 95th percentile. Too blunt — hurts Haaland more than it helps. Consider a softer approach (e.g., cap at p97, or only cap if the player's most recent season shows decline).

The three recommended changes are complementary (they address different aspects: transfer handling, appearance points, and point conversion). They should be combined and re-tested on a merged branch.

## Squad Selector Backtest: 2025 GW 1-8

Classic FPL opening squad chosen by `cmd/squad` from `-backtest` projections,
then scored against actual FPL points with a fixed XI, FPL automatic
substitutions, and the captain re-picked every gameweek from the model's
own per-gameweek projections (vice takes over on a blank).

**Team assignment.** Rates come only from seasons before 2025. Each player
is assigned the club they were at when the summer window closed, computed
by `loadTeamCutoffDate` as `date_trunc('year', min(fixture_date)) + 8
months` — **2025-09-01** for this season — then taking the `players_teams`
row spanning that date. Summer transfers are therefore captured and January
moves are not, so there is no lookahead. Prices are GW1 values from
`fpl_players_gameweeks` (`fpl_season_info.starting_price` is only populated
for 2026). The evaluation pool is built with the same cutoff logic and
restricted to clubs with 2025 PL fixtures.

| Squad | Points | Cost | Formation | Captain |
|---|---|---|---|---|
| **Projection model** | **431** | £100.0M | 4-5-1 | Salah |
| Baseline: 2024 FPL points | 335 | £100.0M | 5-2-3 | Salah |
| Hindsight optimum | 673 | £87.5M | 5-3-2 | Haaland |
| Random valid squads (n=200) | 159 avg (43-311) | — | — | — |

- **+96 pts vs the naive "last season's points" baseline** (+29%)
- **+272 pts vs a random valid squad**
- **64% of the hindsight optimum**
- 53.9 pts/gameweek, in line with a competent real-world FPL manager
- Per-gameweek: 76, 27, 60, 64, 54, 56, 32, 62

### Accuracy of the selected XI

The XI was projected at 368.9 points and scored 350 (**−5.1%**), so the
window scaling and fixture adjustment are well calibrated in aggregate.
Automatic substitutions added 45 points (Gudmundsson 24, Kroupi 21), and
the captain added 36.

| Player | Projected | Actual | Error |
|---|---|---|---|
| Erling Haaland | 44.8 | 83 | +38.2 |
| Omar Alderete | 21.5 | 45 | +23.5 |
| Joe Rodon | 19.0 | 31 | +12.0 |
| David Raya | 31.4 | 40 | +8.6 |
| Bruno Fernandes | 35.7 | 36 | +0.3 |
| Bryan Mbeumo | 35.8 | 36 | +0.2 |
| Tijjani Reijnders | 32.2 | 32 | −0.2 |
| Luke O'Nien | 19.0 | 0 | −19.0 |
| Jaka Bijol | 19.4 | 0 | −19.4 |
| Mohamed Salah | 63.6 | 36 | −27.6 |
| Cole Palmer | 46.5 | 11 | −35.5 |

### Captaincy

| Policy | Points |
|---|---|
| Fixed captain for the whole window | 431 |
| Re-picked weekly from model projections | 431 (+0) |
| Re-picked weekly with hindsight | 485 (+54) |

Re-picking the captain each week using the model's own per-gameweek numbers
changes **nothing**: the model rates Salah 6.9–9.0 per gameweek against
Haaland's 4.8–6.1, a gap far wider than any fixture-difficulty swing, so
Salah is chosen all eight weeks. The 54-point shortfall against a perfect
captain is therefore entirely a projection-accuracy problem, not a
captain-policy problem. Fixture adjustment is too weak a signal to overturn
a mis-ranked player.

### Where the points were lost

1. **Budget enablers that lost their place (−38 pts)**: Bijol and O'Nien
   were cheap £4.0M picks bought to free up money for the premium players
   — both projected ~19 points and played zero minutes. With the backup
   keeper, £12.0M of the squad returned nothing. The optimiser maximises
   expected points with no penalty for rotation risk, so it gravitates to
   cheap projected starters at newly promoted clubs, which is exactly where
   minutes are least certain.
2. **Premium midfield busts (−63 pts vs projection)**: Salah and Palmer
   accounted for £25.0M (25% of budget) and returned 47 points between
   them. Salah alone drove the captaincy shortfall above.

The hindsight optimum spent only £87.5M, confirming that the budget is not
the binding constraint — player selection is.

### Reproducing

```
go run ./cmd/project/ -season 2025 -backtest -output out/proj_2025.json
go run ./cmd/squad/ -input out/proj_2025.json -season 2025 -gameweeks 8 \
    -json out/squad_2025_gw8.json
python3 scratch/backtest_squad.py
```

The Python script needs `out/pool_2025.csv`, `out/gwpts_2025.csv` and
`out/prior_2024.csv`; the `\copy` queries that build them are in the git
history for this section.

## Todos

### High impact

- **Rotation risk penalty in squad selection**: The squad optimiser treats a
  cheap projected starter as risk-free. Discounting players whose projected
  minutes rely on a single prior season, or who moved club in the summer,
  would have avoided Bijol and O'Nien. A variance-aware objective (maximise
  expected points minus k × minutes uncertainty) is the natural fix.
- **Captaincy is bounded by projection quality**: weekly re-picking is
  already implemented and gains nothing, because fixture adjustment cannot
  overturn a mis-ranked player. The 54-point gap to a perfect captain closes
  only by improving the underlying points model for premium attackers.

- **FPL gameweek-level points model**: Rather than converting xG/xA to FPL points via the scoring rules, train a regression directly from historical per-90 stats to actual FPL gameweek points. This would implicitly learn the correct bonus/DEFCON/save relationships rather than modelling each component separately with hand-tuned calibrations.
- **Position-specific minutes model**: Forwards average 66 min/app, midfielders 74, defenders 82, GKs 90. The current appearance points model uses a crude fullMatchFrac estimate. A position-aware model would improve this.
- **Transfer detection**: WHen a player is in the FPL player pool for the first time in the target season, ensure that we check to see whether we have historic data for them in other leagues, and use that if we do. If we have no prior data for them, assume that they will play a decent number of minutes and use their team's historic performances to predict their performance.
- **Strengthen regression for extreme rates**: Salah's 0.74 xG/90 is barely regressed because he has thousands of historical minutes. A harder cap on maximum per-90 rates (e.g. 95th percentile for the position) would prevent runaway projections for outlier players.

### Medium impact

- **Previous-season FPL points as a feature**: For players with FPL gameweek history, their actual prior-season total is a strong predictor and isn't currently used at all. Blending historical FPL points with the xG-based projection could improve accuracy for established players.
- **Better clean sheet model**: The current blend of actual CS rates (70%) and Poisson xGA (30%) still slightly over-projects defenders and GKs. A model that accounts for squad changes (new signings, departures) affecting defensive quality would help.
- **Fixture difficulty weighting**: The model treats all 38 PL matches equally. Early-round draft picks could be improved by weighting the first ~10 gameweeks more heavily (since waiver pickups can fix later problems).
- **Multi-season FPL points variance**: The consistency metric currently uses raw gameweek points variance from FPL history. A better approach would decompose variance into "player skill variance" vs "matchup variance" to get a truer H2H floor estimate.

### Lower impact

- **Age-based decline curves**: Without birth dates in the DB, we can't model age decline. Adding birth dates would allow applying position-specific aging curves (forwards decline faster than defenders).
- **Penalty taker identification**: Penalty goals are worth the same as open-play goals in FPL but are much more predictable. Identifying designated penalty takers and adding expected penalty goals would improve projections for those players.
- **Backup GK detection**: Several GKs are projected as starters but are actually backups (scoring 0-13 actual points). Using squad hierarchy data or minutes trends to identify likely #2 keepers would reduce GK over-projections.
- **Competition for minutes within a squad**: The model projects each player independently. Modelling competition (e.g. two strikers competing for one spot) would improve minutes projections for rotation-risk players.
