package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

// constStrategy always returns the same signal.
type constStrategy float64

func (c constStrategy) Spec() strategy.Spec { return strategy.Spec{ID: "const"} }
func (c constStrategy) Signal([]market.Bar) strategy.Opinion {
	return strategy.Opinion{Signal: float64(c)}
}

// peekStrategy fails the test if it ever sees more bars than allowed.
type peekStrategy struct {
	t    *testing.T
	seen *int
}

func (p peekStrategy) Spec() strategy.Spec { return strategy.Spec{ID: "peek"} }
func (p peekStrategy) Signal(h []market.Bar) strategy.Opinion {
	if cap(h) != len(h) {
		p.t.Fatalf("history slice exposes future bars: len %d cap %d", len(h), cap(h))
	}
	*p.seen = len(h)
	return strategy.Opinion{}
}

func bars(closes ...float64) []market.Bar {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]market.Bar, len(closes))
	for i, c := range closes {
		out[i] = market.Bar{Time: t0.AddDate(0, 0, i), Open: c, High: c, Low: c, Close: c}
	}
	return out
}

func TestRunLongNoCosts(t *testing.T) {
	// Opens equal closes, so returns come from the overnight gap only.
	b := bars(100, 110, 121, 133.1)
	res, err := Run(b, constStrategy(1), Config{AllowShort: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// t=0 decide long → fill at open of bar1 (110): no return on bar1.
	// t=1 hold → gap 110→121 = +10 % on bar2. t=2 is the last decision →
	// flat at the open of bar3, after earning the 121→133.1 gap (+10 %).
	want := []float64{0, 0.1, 0.1}
	for i, w := range want {
		if math.Abs(res.Returns[i]-w) > 1e-12 {
			t.Fatalf("returns = %v, want %v", res.Returns, want)
		}
	}
	if res.Trades != 2 || res.Turnover != 2 {
		t.Fatalf("trades=%d turnover=%v, want 2/2", res.Trades, res.Turnover)
	}
}

func TestRunCosts(t *testing.T) {
	b := bars(100, 100, 100, 100)
	res, _ := Run(b, constStrategy(1), Config{FeeBps: 10, SlippageBps: 5}, 0)
	got := TotalReturn(res.Returns)
	want := (1-0.0015)*(1-0.0015) - 1
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("total return %v, want %v (one round trip)", got, want)
	}
}

func TestShortDisallowed(t *testing.T) {
	res, _ := Run(bars(100, 90, 80, 70), constStrategy(-1), Config{}, 0)
	if res.Trades != 0 {
		t.Fatalf("short signal must be ignored when shorting is not allowed")
	}
}

func TestNoLookahead(t *testing.T) {
	seen := 0
	b := bars(1, 2, 3, 4, 5, 6)
	if _, err := Run(b, peekStrategy{t, &seen}, Config{}, 1); err != nil {
		t.Fatal(err)
	}
	if seen != len(b)-2 {
		t.Fatalf("last decision saw %d bars, want %d", seen, len(b)-2)
	}
}

func TestWalkForwardFolds(t *testing.T) {
	b := bars(make([]float64, 100)...)
	for i := range b {
		b[i].Open, b[i].High, b[i].Low, b[i].Close = 1, 1, 1, 1
	}
	folds, err := RunWalkForward(b, Fixed(constStrategy(0)), Config{}, WalkForward{Train: 40, Test: 25})
	if err != nil {
		t.Fatal(err)
	}
	// test segments: [40,65) [65,90) [90,100)
	if len(folds) != 3 {
		t.Fatalf("got %d folds, want 3", len(folds))
	}
	total := 0
	for _, f := range folds {
		total += len(f.Result.Returns)
	}
	if total != 60 {
		t.Fatalf("out-of-sample returns = %d, want 60", total)
	}
	if _, err := RunWalkForward(b[:41], Fixed(constStrategy(0)), Config{}, WalkForward{Train: 40, Test: 25}); err == nil {
		t.Fatal("expected ErrInsufficientData")
	}
}

func TestMetrics(t *testing.T) {
	if dd := MaxDrawdown([]float64{0.1, -0.5, 0.2}); math.Abs(dd-0.5) > 1e-12 {
		t.Fatalf("MaxDrawdown = %v", dd)
	}
	if s, _, _ := Sharpe([]float64{0.01, 0.01}, 365); s != 0 {
		t.Fatalf("zero-variance Sharpe should be 0, got %v", s)
	}
}
