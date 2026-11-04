// Package strategy implements layer 3: a catalogue of declarative strategies.
// A strategy emits a normalised opinion; it never decides nor allocates.
package strategy

import (
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

// Opinion is the normalised output of a strategy.
type Opinion struct {
	// Signal in [-1, 1]: -1 = fully short, 0 = flat, +1 = fully long.
	Signal float64 `json:"signal"`
	// Confidence in [0, 1].
	Confidence float64 `json:"confidence"`
	// Trace holds the intermediate values that produced Signal.
	Trace map[string]float64 `json:"trace,omitempty"`
}

// Requirements are the prerequisites for a (market, strategy) pair to be valid.
type Requirements struct {
	MinCapital    int64                `json:"min_capital"`
	Granularities []market.Granularity `json:"granularities,omitempty"` // empty = any
	Kinds         []market.Kind        `json:"kinds,omitempty"`         // empty = any
	// Lookback is the number of bars needed before Signal is meaningful.
	Lookback int `json:"lookback"`
}

// Spec is the declarative description of a strategy instance.
type Spec struct {
	ID          string             `json:"id"`
	Description string             `json:"description"`
	Consumes    []string           `json:"consumes"`
	Requires    Requirements       `json:"requires"`
	Params      map[string]float64 `json:"params"`
}

// Strategy computes an opinion from past bars only. history ends at the
// current bar; implementations must not retain or mutate it.
type Strategy interface {
	Spec() Spec
	Signal(history []market.Bar) Opinion
}

// Check returns why the strategy cannot run on the market (empty = valid).
func Check(spec Spec, d market.Descriptor, capital int64) []string {
	var r []string
	req := spec.Requires
	if capital < req.MinCapital {
		r = append(r, fmt.Sprintf("capital %d below strategy minimum %d", capital, req.MinCapital))
	}
	if len(req.Granularities) > 0 && !slices.Contains(req.Granularities, d.Granularity) {
		r = append(r, fmt.Sprintf("granularity %s not in %v", d.Granularity, req.Granularities))
	}
	if len(req.Kinds) > 0 && !slices.Contains(req.Kinds, d.Kind) {
		r = append(r, fmt.Sprintf("market kind %s not in %v", d.Kind, req.Kinds))
	}
	return r
}

// Factory builds a strategy from parameters (missing ones take defaults).
type Factory func(params map[string]float64) (Strategy, error)

var catalog = map[string]Factory{
	SMACrossID:     NewSMACross,
	ZScoreRevertID: NewZScoreRevert,
}

// New instantiates a catalogue strategy.
func New(id string, params map[string]float64) (Strategy, error) {
	f, ok := catalog[id]
	if !ok {
		return nil, fmt.Errorf("unknown strategy %q (available: %v)", id, IDs())
	}
	return f(params)
}

// IDs lists catalogue identifiers, sorted.
func IDs() []string { return slices.Sorted(maps.Keys(catalog)) }

// mergeParams overlays given on defaults and rejects unknown keys.
func mergeParams(defaults, given map[string]float64) (map[string]float64, error) {
	out := maps.Clone(defaults)
	for k, v := range given {
		if _, ok := defaults[k]; !ok {
			return nil, fmt.Errorf("unknown parameter %q (allowed: %v)", k, slices.Sorted(maps.Keys(defaults)))
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("parameter %q must be finite", k)
		}
		out[k] = v
	}
	return out, nil
}

func intParam(p map[string]float64, k string, min int) (int, error) {
	v := p[k]
	if v != math.Trunc(v) || v < float64(min) {
		return 0, fmt.Errorf("parameter %q must be an integer >= %d, got %v", k, min, v)
	}
	return int(v), nil
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

func meanClose(bars []market.Bar) float64 {
	s := 0.0
	for _, b := range bars {
		s += b.Close
	}
	return s / float64(len(bars))
}
