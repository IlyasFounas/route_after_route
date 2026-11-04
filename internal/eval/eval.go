// Package eval implements layer 4: one score per valid (market, strategy) pair.
//
// Criterion (single, explicit): see CriterionDoc.
package eval

import (
	"errors"
	"fmt"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/backtest"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

const (
	Criterion    = "oos_sharpe"
	CriterionDoc = "Annualised Sharpe ratio (risk-free rate 0) of walk-forward out-of-sample returns, " +
		"net of the market's taker fees and the configured slippage, pooled over every symbol " +
		"tradable during the period, delisted ones included (survivorship-bias guard)."
)

// Options are the evaluation settings. Fees and short permission are taken
// from the market Descriptor, never from the user.
type Options struct {
	Backtest    backtest.Config
	WalkForward backtest.WalkForward
	From, To    time.Time
}

// Score is the result of the evaluation, with every intermediate value.
type Score struct {
	Criterion string             `json:"criterion"`
	Value     float64            `json:"value"`
	Trace     map[string]float64 `json:"trace"`
}

// ExecutionConfig is the backtest config actually used for market d.
func ExecutionConfig(d market.Descriptor, base backtest.Config) backtest.Config {
	base.FeeBps = d.Fees.TakerBps
	base.AllowShort = d.Access.ShortAllowed
	return base
}

// Evaluate scores strategy s on market d. bars maps symbol → history.
func Evaluate(d market.Descriptor, bars map[string][]market.Bar, s strategy.Strategy, opt Options) (Score, error) {
	if lb := s.Spec().Requires.Lookback; opt.WalkForward.Train < lb {
		return Score{}, fmt.Errorf("train_bars %d shorter than strategy lookback %d", opt.WalkForward.Train, lb)
	}
	cfg := ExecutionConfig(d, opt.Backtest)
	var (
		pooled                         []float64
		trades, folds, used, delisted  int
		skipped                        int
		turnover, costs, maxDD, sumRet float64
	)
	for _, sym := range d.Symbols {
		if !sym.OverlapsPeriod(opt.From, opt.To) {
			continue
		}
		fs, err := backtest.RunWalkForward(bars[sym.Name], backtest.Fixed(s), cfg, opt.WalkForward)
		if errors.Is(err, backtest.ErrInsufficientData) {
			skipped++
			continue
		}
		if err != nil {
			return Score{}, fmt.Errorf("%s: %w", sym.Name, err)
		}
		used++
		if sym.Delisted != nil {
			delisted++
		}
		var symRets []float64
		for _, f := range fs {
			folds++
			trades += f.Result.Trades
			turnover += f.Result.Turnover
			costs += f.Result.Costs
			symRets = append(symRets, f.Result.Returns...)
		}
		pooled = append(pooled, symRets...)
		sumRet += backtest.TotalReturn(symRets)
		maxDD = max(maxDD, backtest.MaxDrawdown(symRets))
	}
	if len(pooled) < 2 {
		return Score{}, fmt.Errorf("no out-of-sample data (%d symbols skipped for insufficient history)", skipped)
	}
	ppy := d.PeriodsPerYear()
	sharpe, mean, std := backtest.Sharpe(pooled, ppy)
	return Score{
		Criterion: Criterion,
		Value:     sharpe,
		Trace: map[string]float64{
			"sharpe":                  sharpe,
			"mean_return":             mean,
			"stdev_return":            std,
			"periods_per_year":        ppy,
			"oos_periods":             float64(len(pooled)),
			"folds":                   float64(folds),
			"symbols":                 float64(used),
			"symbols_delisted":        float64(delisted),
			"symbols_skipped":         float64(skipped),
			"trades":                  float64(trades),
			"turnover":                turnover,
			"costs":                   costs,
			"max_drawdown":            maxDD,
			"avg_symbol_total_return": sumRet / float64(used),
			"fee_bps":                 cfg.FeeBps,
			"slippage_bps":            cfg.SlippageBps,
		},
	}, nil
}
