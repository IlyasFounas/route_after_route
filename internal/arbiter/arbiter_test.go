package arbiter

import (
	"math"
	"testing"
)

func cand(m, s string, score float64) Candidate {
	return Candidate{Market: m, Strategy: s, Score: score, Trace: map[string]float64{"max_drawdown": 0.1}}
}

func TestDecideFiltersAndRanks(t *testing.T) {
	cands := []Candidate{
		cand("m", "low", 0.5),
		cand("m", "high", 2),
		{Market: "m", Strategy: "invalid", Score: 9, Invalid: []string{"missing data"}},
		cand("m", "negative", -1),
		{Market: "m", Strategy: "risky", Score: 5, Trace: map[string]float64{"max_drawdown": 0.6}},
		cand("m", "mid", 1),
	}
	dec, err := Decide(cands, 10000, Policy{MaxActive: 2, Rule: RuleEqual, MaxDrawdown: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Selected) != 2 || dec.Selected[0].Strategy != "high" || dec.Selected[1].Strategy != "mid" {
		t.Fatalf("unexpected selection %+v", dec.Selected)
	}
	if dec.Selected[0].Amount != 5000 || dec.Cash != 0 {
		t.Fatalf("equal split wrong: %+v cash %d", dec.Selected, dec.Cash)
	}
	if len(dec.Rejected) != 4 {
		t.Fatalf("want 4 rejections, got %+v", dec.Rejected)
	}
}

func TestProportional(t *testing.T) {
	dec, _ := Decide([]Candidate{cand("a", "s", 3), cand("b", "s", 1)}, 1000, Policy{MaxActive: 5, Rule: RuleProportional})
	if dec.Selected[0].Amount != 750 || dec.Selected[1].Amount != 250 {
		t.Fatalf("got %+v", dec.Selected)
	}
}

func TestCappedRedistributes(t *testing.T) {
	dec, _ := Decide([]Candidate{cand("a", "s", 8), cand("b", "s", 1), cand("c", "s", 1)}, 1000, Policy{MaxActive: 5, Rule: RuleCapped, Cap: 0.5})
	w := []float64{0.5, 0.25, 0.25}
	for i, a := range dec.Selected {
		if math.Abs(a.Weight-w[i]) > 1e-9 {
			t.Fatalf("weights = %+v, want %v", dec.Selected, w)
		}
	}
	// Single candidate capped at 40 %: the rest stays as cash.
	dec, _ = Decide([]Candidate{cand("a", "s", 1)}, 1000, Policy{MaxActive: 5, Rule: RuleCapped, Cap: 0.4})
	if dec.Selected[0].Amount != 400 || dec.Cash != 600 {
		t.Fatalf("got %+v cash %d", dec.Selected, dec.Cash)
	}
}

func TestMinCapitalPromotesNext(t *testing.T) {
	a := cand("a", "s", 3)
	b := cand("b", "s", 2)
	b.MinCapital = 600
	c := cand("c", "s", 1)
	dec, _ := Decide([]Candidate{a, b, c}, 1000, Policy{MaxActive: 2, Rule: RuleEqual})
	if len(dec.Selected) != 2 || dec.Selected[1].Market != "c" {
		t.Fatalf("b should be dropped and c promoted: %+v", dec.Selected)
	}
}

func TestPolicyValidation(t *testing.T) {
	for _, p := range []Policy{{MaxActive: 0, Rule: RuleEqual}, {MaxActive: 1, Rule: "x"}, {MaxActive: 1, Rule: RuleCapped}} {
		if _, err := Decide(nil, 1, p); err == nil {
			t.Errorf("%+v: expected error", p)
		}
	}
}
