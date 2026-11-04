// Package risk sizes the bracket of a short trade: the take-profit and the
// stop-loss that, over the recent history, gave the best expected net return
// within the user's maximum holding time.
//
// Every past start bar is replayed as if a position had been opened at its
// close and held at most `hold` bars. For each window, the first bar where
// each candidate take-profit and each candidate stop is touched is recorded
// (from highs and lows), then the window's outcome under every (TP, SL) pair
// follows: take-profit first, stop first, or neither (closed at the max-hold
// bar). When both are touched on the same bar, the stop is assumed first:
// the order inside a bar is unknown.
//
// The statistics are unconditional: they describe how this market moves
// over the holding time, not what the strategy's signal adds to it.
package risk

import (
	"math"
	"slices"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

// MinSamples is the fewest past windows an estimate needs.
const MinSamples = 30

// quantiles are the candidate levels, as quantiles of the excursions.
var quantiles = []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9}

// Bracket is the exit plan of one entry. Returns are fractions of the
// amount committed.
type Bracket struct {
	// OK is false when there is no positive expected net return within the
	// holding time (or too little history): the entry should not be taken.
	OK         bool
	TakeProfit float64 // close when the gain reaches it
	StopLoss   float64 // close when the loss reaches it
	// HitRate is the share of past windows that reached the take-profit
	// before the stop; StopRate the share stopped first.
	HitRate, StopRate float64
	// Expected is the mean net return over the past windows (costs
	// included) with this bracket.
	Expected float64
	Samples  int
}

// Params are the inputs of Estimate.
type Params struct {
	// Direction is +1 (long) or -1 (short).
	Direction int
	// Hold is the maximum holding time, in bars.
	Hold int
	// Stride replays one start bar out of Stride (1 = every bar).
	Stride int
	// MaxStop caps the stop-loss: the user's risk setting.
	MaxStop float64
	// CostBps is the trading cost per side (fees + slippage).
	CostBps float64
}

// Estimate returns the best bracket over bars (closed candles, oldest
// first). It runs in two passes over the windows, without storing them:
// the excursions first (to choose the candidate levels), then the first bar
// at which each candidate is touched.
func Estimate(bars []market.Bar, p Params) Bracket {
	if p.Hold < 1 || p.Direction == 0 || p.MaxStop <= 0 {
		return Bracket{}
	}
	stride := max(p.Stride, 1)
	var starts []int
	var mfe, mae []float64
	for i := 0; i+p.Hold < len(bars); i += stride {
		if bars[i].Close <= 0 {
			continue
		}
		f, a := excursions(bars[i:i+p.Hold+1], p.Direction)
		starts, mfe, mae = append(starts, i), append(mfe, f), append(mae, a)
	}
	n := len(starts)
	if n < MinSamples {
		return Bracket{Samples: n}
	}
	tps := levels(mfe, math.Inf(1))
	sls := levels(mae, p.MaxStop)
	if !slices.Contains(sls, p.MaxStop) {
		sls = append(sls, p.MaxStop) // the user's own stop is always a candidate
	}
	slices.Sort(sls)
	if len(tps) == 0 {
		return Bracket{Samples: n}
	}
	// hitTP[w*len(tps)+j] is the first bar of window w reaching tps[j] (-1:
	// never); the same for the stops.
	hitTP := make([]int32, n*len(tps))
	hitSL := make([]int32, n*len(sls))
	end := make([]float64, n)
	for w, i := range starts {
		end[w] = firstHits(bars[i:i+p.Hold+1], p.Direction, tps, sls, hitTP[w*len(tps):(w+1)*len(tps)], hitSL[w*len(sls):(w+1)*len(sls)])
	}
	cost := 2 * p.CostBps / 1e4
	best := Bracket{Expected: math.Inf(-1), Samples: n}
	for j, tp := range tps {
		for k, sl := range sls {
			var sum float64
			var hit, stop int
			for w := range n {
				ti, si := hitTP[w*len(tps)+j], hitSL[w*len(sls)+k]
				switch {
				case si >= 0 && (ti < 0 || si <= ti):
					sum -= sl
					stop++
				case ti >= 0:
					sum += tp
					hit++
				default:
					sum += end[w]
				}
			}
			fn := float64(n)
			if ev := sum/fn - cost; ev > best.Expected {
				best = Bracket{TakeProfit: tp, StopLoss: sl, HitRate: float64(hit) / fn, StopRate: float64(stop) / fn, Expected: ev, Samples: n}
			}
		}
	}
	best.OK = best.Expected > 0
	return best
}

// moves are the favourable and adverse moves of bar b for an entry at
// entry in direction dir.
func moves(b market.Bar, entry float64, dir int) (fav, adv float64) {
	up, down := b.High/entry-1, 1-b.Low/entry
	if dir < 0 {
		return down, up
	}
	return up, down
}

// excursions are the largest favourable and adverse moves of an entry at
// the close of win[0], over the following bars.
func excursions(win []market.Bar, dir int) (mfe, mae float64) {
	entry := win[0].Close
	for _, b := range win[1:] {
		f, a := moves(b, entry, dir)
		mfe, mae = max(mfe, f), max(mae, a)
	}
	return mfe, mae
}

// firstHits fills, for each sorted level, the first bar of the window that
// reaches it (-1: never), and returns the return at the last bar.
func firstHits(win []market.Bar, dir int, tps, sls []float64, hitTP, hitSL []int32) float64 {
	for j := range hitTP {
		hitTP[j] = -1
	}
	for k := range hitSL {
		hitSL[k] = -1
	}
	entry := win[0].Close
	nextTP, nextSL := 0, 0
	var runF, runA float64
	for t, b := range win[1:] {
		f, a := moves(b, entry, dir)
		runF, runA = max(runF, f), max(runA, a)
		for nextTP < len(tps) && runF >= tps[nextTP] {
			hitTP[nextTP] = int32(t)
			nextTP++
		}
		for nextSL < len(sls) && runA >= sls[nextSL] {
			hitSL[nextSL] = int32(t)
			nextSL++
		}
	}
	return float64(dir) * (win[len(win)-1].Close/entry - 1)
}

// levels are the distinct positive quantiles of xs, capped, sorted.
func levels(xs []float64, cap float64) []float64 {
	xs = slices.Clone(xs)
	slices.Sort(xs)
	var out []float64
	for _, q := range quantiles {
		v := min(xs[int(q*float64(len(xs)-1))], cap)
		if v > 0 && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
