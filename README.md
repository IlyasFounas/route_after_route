# route_after_route

A decision-making algorithm for public markets.

You give it your situation: capital, available time, skills and risk tolerance. Then you pick one or more public markets. It evaluates the available strategies, ranks them, and returns an ordered list of actions. Each action comes with its score and the numbers behind it.

**It decides, it does not execute.** It reads public market data and never places an order. Nothing is centralized: you clone the repo and run it yourself.

## How it works

Each run is one decision cycle:

1. **Profile.** Your situation is read and validated strictly. Your risk tolerance sets the maximum drawdown you accept and a stop-loss per position.
2. **Markets.** The markets you selected are loaded. The ones your profile can't access are filtered out, for example because your capital is too low or a required skill is missing.
3. **Strategies.** Each strategy reads the price history and gives an opinion between -1 (short) and +1 (long). It does not decide anything.
4. **Evaluation.** Every (market, strategy) pair is replayed on historical data. The replay is walk-forward and counts fees and slippage. Orders are filled at the next bar's open. Delisted symbols stay in the replay. The pair is scored on its out-of-sample results.
5. **Arbitration.** The algorithm drops invalid or too-risky candidates, ranks the rest by score and splits your capital between the best ones.
6. **Output.** It prints an ordered list of actions and the concrete orders to carry out (open, hold, close), with every intermediate value.
7. **Loop.** Your portfolio is saved locally. On the next cycle, its value becomes your new capital, and positions are closed on stop-loss, deselection or signal reversal.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the contracts of each layer.

## Installation

Requires [Go](https://go.dev/dl/) 1.25 or later. There are no other dependencies: the code uses the standard library only.

```sh
git clone https://github.com/IlyasFounas/route_after_route.git
cd route_after_route
go build -o rar ./cmd/rar
```

## Usage

1. **Fill in your profile** in [`examples/profile.json`](examples/profile.json), or a copy of it.
2. **Select the markets to analyze** in the config file, such as [`examples/config.json`](examples/config.json).
3. **Run the algorithm:**

   ```sh
   # Offline demo on deterministic synthetic data
   ./rar run --profile examples/profile.json --config examples/config.json --dry-run

   # Real data: Binance spot public API, no key needed
   ./rar run --profile examples/profile.json --config examples/config.binance.json
   ```

4. **Read the output.** Each line gives a market, a strategy, a suggested allocation and the breakdown of the calculation.

| Command | What it does |
|---|---|
| `rar run` | One full cycle: evaluate, arbitrate, print actions and orders, update `state.json` |
| `rar backtest` | Score every (market, strategy) pair, without arbitration or state |
| `rar markets` | List the selected markets and whether your profile can access them |
| `rar strategies` | Show the declaration of each configured strategy |

Common flags:

- `--format json` gives machine-readable output.
- `--state` chooses the state file.
- `--force` ignores the re-evaluation interval.
- `--dry-run` doesn't write the state file.

`rar run` is meant to be scheduled, with cron or a systemd timer. It does nothing until `reevaluate_every` has elapsed since the last cycle.

### Profile

The schema is fixed. Unknown, missing or out-of-range fields are rejected.

```json
{
  "capital": 1000000,
  "hours_per_week": 4,
  "skills": ["technical_analysis", "statistics"],
  "risk_tolerance": 5
}
```

- `capital` is in **minor units** (cents): `1000000` means 10,000.00.
- `skills` must come from a closed set: `derivatives`, `fundamental_analysis`, `programming`, `statistics`, `technical_analysis`.
- `risk_tolerance` is an integer from 1 to 10. It sets two limits:
  - the maximum accepted drawdown, equal to 5 % × tolerance;
  - the stop-loss, equal to half that drawdown.

### Configuration

Fields left out take the default values shown below.

```jsonc
{
  "markets": [                                     // your selection (required)
    { "type": "csvfile", "options": { "path": "../testdata/trend" } },
    { "type": "binance", "options": { "symbols": ["BTCUSDT"], "interval": "1d" } }
  ],
  "strategies": [                                  // default: the whole catalogue
    { "id": "sma_cross" },
    { "id": "sma_cross", "name": "sma_fast", "params": { "fast": 5, "slow": 20 } }
  ],
  "period":   { "lookback_days": 730 },            // or { "from": "2020-01-01", "to": "2022-09-27" }
  "backtest": { "slippage_bps": 5, "position_step": 0.25, "train_bars": 250, "test_bars": 60 },
  "policy":   { "max_active": 3, "rule": "capped", "cap": 0.5, "min_score": 0 },
  "reevaluate_every": "24h"
}
```

Relative paths are resolved from the config file's directory.

## Output

The output is deliberately traceable. For every suggested action you can trace back to the signal and the intermediate values that produced it. Every rejected candidate and every filtered-out market is listed with its reason. No decision is opaque.

This excerpt comes from the offline demo:

```
ACTIONS
  #  MARKET      STRATEGY       SCORE   WEIGHT  ALLOCATION
  1  demo-range  zscore_revert  2.0601  50.0%   5000.00

  [1] demo-range / zscore_revert — trace:
      max_drawdown             0.0409208
      mean_return              0.000557214
      sharpe                   2.0601
      trades                   311
      ...
    RANGE  signal=+0.140 exposure=+0.25 long  alloc=1250.00 price=98.8743  {mean=101.4 stdev=2.077 z=-1.21}

ORDERS
  OPEN  long  demo-range/RANGE  zscore_revert  1250.00  @98.8743    rank 1, signal +0.140, exposure +0.25

REJECTED CANDIDATES
  demo-trend / sma_cross (score 0.6052): max drawdown 30.3% exceeds risk limit 25.0%
  ...
```

## Make it yours

The algorithm is meant to be rewritten. Add your own strategies, plug in another market, change what the scoring maximizes, swap the allocation rule: nothing here is meant to be used as-is. The layers are kept separate precisely so you can replace one without touching the rest. A test (`internal/archtest`) fails if a layer starts depending on something it shouldn't.

| To change… | Look at |
|---|---|
| Add a strategy | [`internal/strategy/`](internal/strategy/): implement `Strategy` (`Spec()` + `Signal()`) and register it in the catalogue |
| Plug in a market | [`internal/market/`](internal/market/): implement `Market` (`Descriptor()` + `Bars()`), call `market.Register` in `init()`, and import it in [`cmd/rar`](cmd/rar/main.go). See `csvfile` and `binance` for examples. |
| Change what the score maximizes | [`internal/eval/`](internal/eval/eval.go) |
| Swap the allocation rule | [`internal/arbiter/`](internal/arbiter/arbiter.go) |
| Change exit conditions | [`internal/loop/`](internal/loop/loop.go) |

Before proposing a change, run the checks:

```sh
go vet ./... && go test ./...                     # unit tests, layer rules, golden report
go test ./internal/app -update                    # regenerate the golden report after an intended change
go test ./internal/profile -fuzz FuzzParse -fuzztime 30s
go run ./tools/gendata                            # regenerate the synthetic demo data (fixed seed)
```

If your version works better, open a pull request.

## Disclaimer

This tool produces suggestions, not guarantees. Backtested performance does not predict future results. Markets carry a risk of capital loss, including total loss. Review every output before acting on it, and only commit money you can afford to lose.

## License

(to be filled in)
