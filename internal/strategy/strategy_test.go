package strategy

import (
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

func series(closes ...float64) []market.Bar {
	out := make([]market.Bar, len(closes))
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range closes {
		out[i] = market.Bar{Time: t0.AddDate(0, 0, i), Open: c, High: c, Low: c, Close: c}
	}
	return out
}

func TestSMACross(t *testing.T) {
	s, err := New(SMACrossID, map[string]float64{"fast": 2, "slow": 4})
	if err != nil {
		t.Fatal(err)
	}
	if o := s.Signal(series(1, 2, 3)); o.Signal != 0 {
		t.Fatal("insufficient history must be neutral")
	}
	if o := s.Signal(series(10, 10, 12, 12)); o.Signal <= 0 {
		t.Fatalf("uptrend should be long, got %v", o.Signal)
	}
	if o := s.Signal(series(12, 12, 10, 10)); o.Signal >= 0 {
		t.Fatalf("downtrend should be short, got %v", o.Signal)
	}
}

func TestZScoreRevert(t *testing.T) {
	s, err := New(ZScoreRevertID, map[string]float64{"window": 5})
	if err != nil {
		t.Fatal(err)
	}
	if o := s.Signal(series(10, 10, 10, 10, 20)); o.Signal >= 0 {
		t.Fatalf("spike up should be faded (short), got %v", o.Signal)
	}
	if o := s.Signal(series(10, 10, 10, 10, 10)); o.Signal != 0 {
		t.Fatal("flat series must be neutral")
	}
}

func TestParamValidation(t *testing.T) {
	bad := []struct {
		id string
		p  map[string]float64
	}{
		{SMACrossID, map[string]float64{"fast": 30, "slow": 10}},
		{SMACrossID, map[string]float64{"fast": 1.5}},
		{SMACrossID, map[string]float64{"unknown": 1}},
		{ZScoreRevertID, map[string]float64{"entry": 3, "full": 2}},
		{"nope", nil},
	}
	for _, b := range bad {
		if _, err := New(b.id, b.p); err == nil {
			t.Errorf("%s %v: expected error", b.id, b.p)
		}
	}
}

func TestCheck(t *testing.T) {
	s, _ := New(SMACrossID, nil)
	if r := Check(s.Spec(), market.Descriptor{Granularity: market.Minute, Kind: market.KindCrypto}, 100); len(r) != 1 {
		t.Fatalf("minute granularity should be rejected, got %v", r)
	}
}
