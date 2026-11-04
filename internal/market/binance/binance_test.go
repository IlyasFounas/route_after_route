package binance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

func TestBarsPaginatesAndDropsOpenCandle(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const total = 1500
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		start, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		w.Write([]byte("["))
		n := 0
		for i := 0; i < total && n < pageLimit; i++ {
			open := base.Add(time.Duration(i) * time.Hour).UnixMilli()
			if open < start || open > end {
				continue
			}
			if n > 0 {
				w.Write([]byte(","))
			}
			fmt.Fprintf(w, `[%d,"10","11","9","10.5","100",%d,"0",1,"0","0","0"]`, open, open+3600000-1)
			n++
		}
		w.Write([]byte("]"))
	}))
	defer srv.Close()

	m, err := New(Options{Symbols: []string{"BTCUSDT"}, Interval: market.Hour, BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	// "now" falls inside the last candle → it must be dropped.
	m.now = func() time.Time { return base.Add(time.Duration(total-1)*time.Hour + time.Minute) }
	bars, err := m.Bars(context.Background(), "BTCUSDT", base, base.Add(total*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != total-1 {
		t.Fatalf("got %d bars, want %d", len(bars), total-1)
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want 2", calls)
	}
}

func TestBarsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":-1121,"msg":"Invalid symbol."}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	m, _ := New(Options{Symbols: []string{"NOPE"}, BaseURL: srv.URL}, srv.Client())
	if _, err := m.Bars(context.Background(), "NOPE", time.Unix(0, 0), time.Now()); err == nil {
		t.Fatal("expected error")
	}
}
