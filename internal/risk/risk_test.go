package risk

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// walk is a deterministic random walk of n one-second bars with a drift per
// bar (fraction) and a volatility per bar.
func walk(n int, drift, vol float64, seed uint64) []market.Bar {
	rng := rand.New(rand.NewPCG(seed, seed))
	bars := make([]market.Bar, n)
	p := 100.0
	for i := range bars {
		next := p * math.Exp(drift+vol*rng.NormFloat64())
		hi := math.Max(p, next) * (1 + vol/4*math.Abs(rng.NormFloat64()))
		lo := math.Min(p, next) * (1 - vol/4*math.Abs(rng.NormFloat64()))
		bars[i] = market.Bar{Time: t0.Add(time.Duration(i) * time.Second), Open: p, High: hi, Low: lo, Close: next}
		p = next
	}
	return bars
}

func TestTrendFavoursTheTrendSide(t *testing.T) {
	bars := walk(7200, 0.0002, 0.0005, 1) // +0.02 %/s: about +1.2 % per minute
	p := Params{Direction: 1, Hold: 60, Stride: 1, MaxStop: 0.05, CostBps: 15}
	long := Estimate(bars, p)
	if !long.OK || long.Expected <= 0 || long.TakeProfit <= 0 || long.StopRate > 0.1 {
		t.Fatalf("long in an up trend: %+v", long)
	}
	p.Direction = -1
	if short := Estimate(bars, p); short.OK {
		t.Fatalf("short in an up trend must have no edge: %+v", short)
	}
}

func TestRandomWalkHasNoEdgeAfterCosts(t *testing.T) {
	bars := walk(7200, 0, 0.0005, 2)
	for _, dir := range []int{1, -1} {
		b := Estimate(bars, Params{Direction: dir, Hold: 60, Stride: 1, MaxStop: 0.05, CostBps: 15})
		if b.OK {
			t.Errorf("direction %d: a driftless walk must not beat 0.3 %% of round-trip costs: %+v", dir, b)
		}
		if b.Samples != 7200-60 {
			t.Errorf("samples %d", b.Samples)
		}
	}
}

func TestStopNeverExceedsTheUserStop(t *testing.T) {
	bars := walk(3000, 0.0003, 0.004, 3) // very volatile
	b := Estimate(bars, Params{Direction: 1, Hold: 120, Stride: 2, MaxStop: 0.01, CostBps: 10})
	if b.StopLoss <= 0 || b.StopLoss > 0.01 {
		t.Fatalf("stop %v outside (0, 1%%]: %+v", b.StopLoss, b)
	}
	if b.HitRate+b.StopRate > 1+1e-9 {
		t.Fatalf("rates %+v", b)
	}
}

func TestDeterministicAndStrided(t *testing.T) {
	bars := walk(4000, 0.0001, 0.001, 4)
	p := Params{Direction: 1, Hold: 30, Stride: 1, MaxStop: 0.03, CostBps: 5}
	if a, b := Estimate(bars, p), Estimate(bars, p); !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs differ: %+v vs %+v", a, b)
	}
	p.Stride = 8
	if b := Estimate(bars, p); b.Samples != (4000-30+7)/8 {
		t.Fatalf("stride 8: %d samples", b.Samples)
	}
}

// Every window rises 1 % on its first bar then comes back: a 1 % take-profit
// is always reached before any stop.
func TestKnownPattern(t *testing.T) {
	var bars []market.Bar
	for i := range 400 {
		b := market.Bar{Time: t0.Add(time.Duration(i) * time.Second), Open: 100, High: 100, Low: 100, Close: 100}
		if i%2 == 1 {
			b.High = 101
		}
		bars = append(bars, b)
	}
	got := Estimate(bars, Params{Direction: 1, Hold: 2, Stride: 1, MaxStop: 0.02, CostBps: 10})
	if !got.OK || math.Abs(got.TakeProfit-0.01) > 1e-12 || got.HitRate != 1 || got.StopRate != 0 {
		t.Fatalf("%+v", got)
	}
	if want := 0.01 - 0.002; math.Abs(got.Expected-want) > 1e-12 {
		t.Fatalf("expected %v, want %v", got.Expected, want)
	}
}

func TestTooLittleHistory(t *testing.T) {
	bars := walk(80, 0.001, 0.001, 5)
	if b := Estimate(bars, Params{Direction: 1, Hold: 60, Stride: 1, MaxStop: 0.05}); b.OK || b.Samples != 20 {
		t.Fatalf("%+v", b)
	}
	if b := Estimate(bars, Params{Direction: 1, Hold: 0, MaxStop: 0.05}); b.OK {
		t.Fatal("no holding time, no bracket")
	}
}

func BenchmarkEstimate600s(b *testing.B) {
	bars := walk(7200, 0, 0.0005, 6)
	for b.Loop() {
		Estimate(bars, Params{Direction: 1, Hold: 600, Stride: 1, MaxStop: 0.05, CostBps: 15})
	}
}
