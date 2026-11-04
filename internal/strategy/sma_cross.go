package strategy

import (
	"fmt"
	"maps"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

const SMACrossID = "sma_cross"

// smaCross is a trend-following moving-average crossover.
//
//	gap    = SMA(fast) / SMA(slow) - 1
//	signal = clamp(gap × sensitivity, -1, 1)
type smaCross struct {
	fast, slow  int
	sensitivity float64
	params      map[string]float64
}

func NewSMACross(given map[string]float64) (Strategy, error) {
	p, err := mergeParams(map[string]float64{"fast": 10, "slow": 30, "sensitivity": 20}, given)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SMACrossID, err)
	}
	fast, err := intParam(p, "fast", 1)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SMACrossID, err)
	}
	slow, err := intParam(p, "slow", 2)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SMACrossID, err)
	}
	if slow <= fast {
		return nil, fmt.Errorf("%s: slow (%d) must be > fast (%d)", SMACrossID, slow, fast)
	}
	if p["sensitivity"] <= 0 {
		return nil, fmt.Errorf("%s: sensitivity must be > 0", SMACrossID)
	}
	return &smaCross{fast: fast, slow: slow, sensitivity: p["sensitivity"], params: p}, nil
}

func (s *smaCross) Spec() Spec {
	return Spec{
		ID:          SMACrossID,
		Description: "Trend following: long when the fast SMA is above the slow SMA, short (if allowed) below.",
		Consumes:    []string{"close"},
		Requires: Requirements{
			Lookback: s.slow,
		},
		Params: maps.Clone(s.params),
	}
}

func (s *smaCross) Signal(h []market.Bar) Opinion {
	if len(h) < s.slow {
		return Opinion{}
	}
	f := meanClose(h[len(h)-s.fast:])
	sl := meanClose(h[len(h)-s.slow:])
	gap := f/sl - 1
	return Opinion{
		Signal:     clamp(gap*s.sensitivity, -1, 1),
		Confidence: 1,
		Trace:      map[string]float64{"sma_fast": f, "sma_slow": sl, "gap": gap},
	}
}
