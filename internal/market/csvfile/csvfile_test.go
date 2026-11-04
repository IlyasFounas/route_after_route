package csvfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

func TestBarsWindow(t *testing.T) {
	m, err := New("../../../testdata/trend")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)
	bars, err := m.Bars(context.Background(), "TREND", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 29 || !bars[0].Time.Equal(from) {
		t.Fatalf("got %d bars starting %v", len(bars), bars[0].Time)
	}
	if _, err := m.Bars(context.Background(), "NOPE", from, to); err == nil {
		t.Fatal("unknown symbol must fail")
	}
}

func TestRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	desc := `{"id":"x","kind":"crypto","granularity":"1d","fees":{"taker_bps":1,"maker_bps":1},"min_capital":0,"access":{"short_allowed":false,"requires_account":false,"min_hours_per_week":0},"symbols":[{"name":"A"}],"data_source":"t"}`
	os.WriteFile(filepath.Join(dir, "market.json"), []byte(desc), 0o644)
	os.WriteFile(filepath.Join(dir, "A.csv"), []byte("date,o,h,l,c,v\n"), 0o644)
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Bars(context.Background(), "A", time.Time{}, time.Now()); err == nil {
		t.Fatal("bad header must fail")
	}
	os.WriteFile(filepath.Join(dir, "market.json"), []byte(`{"id":"x","oops":1}`), 0o644)
	if _, err := New(dir); err == nil {
		t.Fatal("unknown descriptor field must fail")
	}
}

func TestReplay(t *testing.T) {
	m, err := New("../../../testdata/trend")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetReplay(ReplayOptions{Start: "2022-09-20", Interval: "0s"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2022, 9, 20, 0, 0, 0, 0, time.UTC)
	past, err := m.Bars(context.Background(), "TREND", time.Time{}, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !past[len(past)-1].Time.Before(start) {
		t.Fatalf("Bars leaked a replay candle: %v", past[len(past)-1].Time)
	}
	out := make(chan market.Update, 100)
	if err := m.Stream(context.Background(), []string{"TREND", "DEAD"}, out, nil); err != nil {
		t.Fatal(err)
	}
	close(out)
	var n, closed int
	var last time.Time
	for u := range out {
		n++
		if u.Symbol == "DEAD" {
			t.Fatal("DEAD is delisted before the replay start: no update expected")
		}
		if u.Closed {
			closed++
			if !u.Bar.Time.After(last) {
				t.Fatal("closed candles out of order")
			}
			last = u.Bar.Time
		}
	}
	// 2022-09-20 .. 2022-09-26 = 7 candles, each sent forming + closed.
	if closed != 7 || n != 14 {
		t.Fatalf("got %d updates (%d closed), want 14 (7)", n, closed)
	}
}
