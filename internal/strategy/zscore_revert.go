package strategy

import (
	"fmt"
	"maps"
	"math"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

const ZScoreRevertID = "zscore_revert"

// zscoreRevert is a mean-reversion strategy on the close z-score.
//
//	z      = (close - mean(window)) / stdev(window)
//	signal = 0                                         if |z| < entry
//	       = -sign(z) × clamp((|z|-entry)/(full-entry), 0, 1) otherwise
type zscoreRevert struct {
	window      int
	entry, full float64
	params      map[string]float64
}

func NewZScoreRevert(given map[string]float64) (Strategy, error) {
	p, err := mergeParams(map[string]float64{"window": 20, "entry": 1, "full": 2.5}, given)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ZScoreRevertID, err)
	}
	w, err := intParam(p, "window", 2)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ZScoreRevertID, err)
	}
	if p["entry"] <= 0 || p["full"] <= p["entry"] {
		return nil, fmt.Errorf("%s: need 0 < entry < full", ZScoreRevertID)
	}
	return &zscoreRevert{window: w, entry: p["entry"], full: p["full"], params: p}, nil
}

func (s *zscoreRevert) Spec() Spec {
	return Spec{
		ID:          ZScoreRevertID,
		Description: "Mean reversion: fade the price when its z-score over the window exceeds the entry threshold.",
		Consumes:    []string{"close"},
		Requires:    Requirements{Lookback: s.window},
		Params:      maps.Clone(s.params),
	}
}

func (s *zscoreRevert) Signal(h []market.Bar) Opinion {
	if len(h) < s.window {
		return Opinion{}
	}
	w := h[len(h)-s.window:]
	mean := meanClose(w)
	ss := 0.0
	for _, b := range w {
		ss += (b.Close - mean) * (b.Close - mean)
	}
	std := math.Sqrt(ss / float64(len(w)-1))
	if std == 0 {
		return Opinion{Confidence: 1, Trace: map[string]float64{"mean": mean, "stdev": 0, "z": 0}}
	}
	z := (w[len(w)-1].Close - mean) / std
	sig := 0.0
	if math.Abs(z) >= s.entry {
		sig = -math.Copysign(clamp((math.Abs(z)-s.entry)/(s.full-s.entry), 0, 1), z)
	}
	return Opinion{
		Signal:     sig,
		Confidence: 1,
		Trace:      map[string]float64{"mean": mean, "stdev": std, "z": z},
	}
}
