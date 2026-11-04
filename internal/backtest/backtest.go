// Package backtest is the test bench for layers 4 and 5: a bar-by-bar
// simulator with fees, slippage and walk-forward splitting.
//
// Anti-lookahead: the decision taken with bars[0..t] is filled at the OPEN
// of bar t+1, and the strategy only ever receives a capacity-limited slice
// bars[:t+1:t+1], so it cannot reach future bars.
package backtest

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

// Config holds execution assumptions.
type Config struct {
	FeeBps      float64 `json:"fee_bps"`
	SlippageBps float64 `json:"slippage_bps"`
	AllowShort  bool    `json:"allow_short"`
	// PositionStep quantises target exposure (e.g. 0.25 → 0, ±0.25, ±0.5…)
	// to avoid paying costs on tiny rebalances. 0 = continuous.
	PositionStep float64 `json:"position_step"`
}

func (c Config) Validate() error {
	if c.FeeBps < 0 || c.SlippageBps < 0 {
		return errors.New("backtest: fees and slippage must be >= 0")
	}
	if c.PositionStep < 0 || c.PositionStep > 1 {
		return errors.New("backtest: position_step must be in [0,1]")
	}
	return nil
}

// Exposure converts a strategy signal into the target exposure in [-1, 1].
func (c Config) Exposure(signal float64) float64 {
	if math.IsNaN(signal) {
		return 0
	}
	x := math.Max(-1, math.Min(1, signal))
	if !c.AllowShort && x < 0 {
		x = 0
	}
	if c.PositionStep > 0 {
		x = math.Round(x/c.PositionStep) * c.PositionStep
	}
	return x + 0 // normalise -0
}

// Result is the per-bar outcome of a simulation.
type Result struct {
	Times    []time.Time // timestamp of the bar each return was earned on
	Returns  []float64   // net simple returns
	Exposure []float64   // exposure held after the fill of each bar
	Trades   int
	Turnover float64 // sum of |Δexposure|
	Costs    float64 // sum of cost fractions paid (fees + slippage)
}

// Run simulates s on bars, taking decisions from index start (inclusive)
// using bars[:start+1] as the first history. Any open position is closed at
// the open of the last bar, so every run ends flat.
func Run(bars []market.Bar, s strategy.Strategy, cfg Config, start int) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	last := len(bars) - 1
	if start < 0 || start >= last {
		return Result{}, fmt.Errorf("backtest: start %d out of range for %d bars", start, len(bars))
	}
	costRate := (cfg.FeeBps + cfg.SlippageBps) / 1e4
	n := last - start
	res := Result{Times: make([]time.Time, 0, n), Returns: make([]float64, 0, n), Exposure: make([]float64, 0, n)}
	pos := 0.0
	for t := start; t < last; t++ {
		target := 0.0
		if t < last-1 {
			target = cfg.Exposure(s.Signal(bars[: t+1 : t+1]).Signal)
		}
		prev, next := bars[t], bars[t+1]
		gap := pos * (next.Open/prev.Close - 1)
		delta := math.Abs(target - pos)
		cost := delta * costRate
		intra := target * (next.Close/next.Open - 1)
		r := (1+gap)*(1-cost)*(1+intra) - 1
		if delta > 1e-12 {
			res.Trades++
			res.Turnover += delta
			res.Costs += cost
		}
		res.Times = append(res.Times, next.Time)
		res.Returns = append(res.Returns, r)
		res.Exposure = append(res.Exposure, target)
		pos = target
	}
	return res, nil
}
