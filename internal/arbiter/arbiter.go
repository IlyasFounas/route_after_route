// Package arbiter implements layer 5, the core: filter invalid candidates,
// rank by score, and split the capital.
package arbiter

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
)

// Rule is the capital allocation rule.
type Rule string

const (
	// RuleEqual gives each selected candidate the same weight.
	RuleEqual Rule = "equal"
	// RuleProportional weights candidates by score.
	RuleProportional Rule = "proportional"
	// RuleCapped is proportional, with no weight above Cap; the excess is
	// redistributed to uncapped candidates, and kept as cash if none remain.
	RuleCapped Rule = "capped"
)

// Policy parameterises arbitration.
type Policy struct {
	MaxActive int     `json:"max_active"`
	Rule      Rule    `json:"rule"`
	Cap       float64 `json:"cap,omitempty"`
	MinScore  float64 `json:"min_score"`
	// MaxDrawdown (fraction) rejects candidates whose backtested drawdown
	// exceeds it. Derived from the profile's risk tolerance; 0 disables.
	MaxDrawdown float64 `json:"max_drawdown"`
}

func (p Policy) Validate() error {
	var errs []error
	if p.MaxActive < 1 {
		errs = append(errs, errors.New("max_active must be >= 1"))
	}
	switch p.Rule {
	case RuleEqual, RuleProportional:
	case RuleCapped:
		if !(p.Cap > 0 && p.Cap <= 1) {
			errs = append(errs, errors.New("cap must be in (0,1] for the capped rule"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown rule %q", p.Rule))
	}
	if p.MaxDrawdown < 0 {
		errs = append(errs, errors.New("max_drawdown must be >= 0"))
	}
	return errors.Join(errs...)
}

// Candidate is one evaluated (market, strategy) pair.
type Candidate struct {
	Market     string             `json:"market"`
	Strategy   string             `json:"strategy"`
	MinCapital int64              `json:"min_capital"`
	Score      float64            `json:"score"`
	Trace      map[string]float64 `json:"trace,omitempty"`
	// Invalid lists reasons computed upstream (prerequisites, data, evaluation).
	Invalid []string `json:"invalid,omitempty"`
}

// Allocation is a selected candidate with its share of capital.
type Allocation struct {
	Candidate
	Weight float64 `json:"weight"`
	Amount int64   `json:"amount"`
}

// Rejection records why a candidate was not selected.
type Rejection struct {
	Market   string   `json:"market"`
	Strategy string   `json:"strategy"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
}

// Decision is the outcome of arbitration. Selected is ordered by rank.
type Decision struct {
	Selected []Allocation `json:"selected"`
	Rejected []Rejection  `json:"rejected"`
	Cash     int64        `json:"cash"`
}

// Decide runs filtering, ranking and allocation. It is deterministic: ties
// are broken by market then strategy identifier.
func Decide(cands []Candidate, capital int64, pol Policy) (Decision, error) {
	if err := pol.Validate(); err != nil {
		return Decision{}, fmt.Errorf("arbiter: %w", err)
	}
	if capital < 0 {
		return Decision{}, errors.New("arbiter: capital must be >= 0")
	}
	var dec Decision
	reject := func(c Candidate, reasons ...string) {
		dec.Rejected = append(dec.Rejected, Rejection{Market: c.Market, Strategy: c.Strategy, Score: c.Score, Reasons: reasons})
	}

	// 1. Filter.
	var eligible []Candidate
	for _, c := range cands {
		if len(c.Invalid) > 0 {
			reject(c, c.Invalid...)
			continue
		}
		var reasons []string
		switch {
		case math.IsNaN(c.Score) || math.IsInf(c.Score, 0):
			reasons = append(reasons, "score is not finite")
		case c.Score < pol.MinScore:
			reasons = append(reasons, fmt.Sprintf("score %.4f below min_score %.4f", c.Score, pol.MinScore))
		case pol.Rule != RuleEqual && c.Score <= 0:
			reasons = append(reasons, fmt.Sprintf("non-positive score %.4f cannot be weighted by score", c.Score))
		}
		if dd, ok := c.Trace["max_drawdown"]; ok && pol.MaxDrawdown > 0 && dd > pol.MaxDrawdown {
			reasons = append(reasons, fmt.Sprintf("max drawdown %.1f%% exceeds risk limit %.1f%%", dd*100, pol.MaxDrawdown*100))
		}
		if len(reasons) > 0 {
			reject(c, reasons...)
			continue
		}
		eligible = append(eligible, c)
	}

	// 2. Rank.
	slices.SortStableFunc(eligible, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.Market, b.Market), cmp.Compare(a.Strategy, b.Strategy))
	})

	// 3. Allocate; drop the lowest-ranked candidate whose share is below its
	// market minimum, then retry with the next one in line.
	for {
		active := eligible[:min(pol.MaxActive, len(eligible))]
		weights := pol.weights(active)
		drop := -1
		amounts := make([]int64, len(active))
		for i := len(active) - 1; i >= 0; i-- {
			amounts[i] = int64(math.Floor(float64(capital) * weights[i]))
			if drop < 0 && amounts[i] < active[i].MinCapital {
				drop = i
			}
		}
		if drop >= 0 {
			c := active[drop]
			reject(c, fmt.Sprintf("allocation %d below market minimum %d", amounts[drop], c.MinCapital))
			eligible = slices.Delete(eligible, drop, drop+1)
			continue
		}
		var spent int64
		for i, c := range active {
			dec.Selected = append(dec.Selected, Allocation{Candidate: c, Weight: weights[i], Amount: amounts[i]})
			spent += amounts[i]
		}
		for i, c := range eligible[len(active):] {
			reject(c, fmt.Sprintf("rank %d beyond max_active=%d", len(active)+i+1, pol.MaxActive))
		}
		dec.Cash = capital - spent
		return dec, nil
	}
}

func (p Policy) weights(c []Candidate) []float64 {
	w := make([]float64, len(c))
	if len(c) == 0 {
		return w
	}
	if p.Rule == RuleEqual {
		for i := range w {
			w[i] = 1 / float64(len(c))
		}
		return w
	}
	sum := 0.0
	for _, x := range c {
		sum += x.Score
	}
	for i, x := range c {
		w[i] = x.Score / sum
	}
	if p.Rule == RuleCapped {
		w = waterFill(w, p.Cap)
	}
	return w
}

// waterFill caps weights at limit and redistributes the excess
// proportionally among uncapped weights until stable.
func waterFill(w []float64, limit float64) []float64 {
	capped := make([]bool, len(w))
	for {
		excess := 0.0
		for i := range w {
			if !capped[i] && w[i] > limit+1e-12 {
				excess += w[i] - limit
				w[i] = limit
				capped[i] = true
			}
		}
		if excess == 0 {
			return w
		}
		free := 0.0
		for i := range w {
			if !capped[i] {
				free += w[i]
			}
		}
		if free == 0 {
			return w // everything capped: the excess stays as cash
		}
		for i := range w {
			if !capped[i] {
				w[i] += excess * w[i] / free
			}
		}
	}
}
