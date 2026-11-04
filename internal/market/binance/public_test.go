package binance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUSDTTickers(t *testing.T) {
	now := time.Now().UnixMilli()
	old := time.Now().Add(-72 * time.Hour).UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/24hr":
			fmt.Fprintf(w, `[
				{"symbol":"SOLUSDT","openPrice":"100","lastPrice":"110","quoteVolume":"5000","closeTime":%[1]d},
				{"symbol":"BTCUSDT","openPrice":"100","lastPrice":"90","quoteVolume":"9000","closeTime":%[1]d},
				{"symbol":"USDCUSDT","openPrice":"1","lastPrice":"1","quoteVolume":"99999","closeTime":%[1]d},
				{"symbol":"BTCUPUSDT","openPrice":"1","lastPrice":"1","quoteVolume":"10","closeTime":%[1]d},
				{"symbol":"ETHBTC","openPrice":"1","lastPrice":"1","quoteVolume":"10","closeTime":%[1]d},
				{"symbol":"DEADUSDT","openPrice":"1","lastPrice":"1","quoteVolume":"10","closeTime":%[2]d},
				{"symbol":"ZEROUSDT","openPrice":"1","lastPrice":"1","quoteVolume":"0","closeTime":%[1]d}
			]`, now, old)
		case "/api/v3/ticker/price":
			fmt.Fprintf(w, `{"symbol":%q,"price":"142.5"}`, r.URL.Query().Get("symbol"))
		}
	}))
	defer srv.Close()
	p := &Public{BaseURL: srv.URL, HTTP: srv.Client()}
	ts, err := p.USDTTickers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Base != "BTC" || ts[1].Base != "SOL" {
		t.Fatalf("got %+v", ts)
	}
	if ts[1].Change < 0.0999 || ts[1].Change > 0.1001 {
		t.Fatalf("change = %v", ts[1].Change)
	}
	px, err := p.Price(context.Background(), "SOLUSDT")
	if err != nil || px != 142.5 {
		t.Fatalf("price %v %v", px, err)
	}
}

func TestKlines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v3/klines" || q.Get("symbol") != "SOLUSDT" || q.Get("interval") != "1s" || q.Get("limit") != "2" {
			t.Errorf("unexpected request %s", r.URL)
		}
		fmt.Fprint(w, `[[1700000000000,"1","2","0.5","1.5","10",1700000000999,"0",1,"0","0","0"],
			[1700000001000,"1.5","1.6","1.4","1.55","3",1700000001999,"0",1,"0","0","0"]]`)
	}))
	defer srv.Close()
	bars, err := (&Public{BaseURL: srv.URL, HTTP: srv.Client()}).Klines(context.Background(), "SOLUSDT", "1s", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || !bars[1].Time.Equal(time.UnixMilli(1700000001000).UTC()) || bars[1].Close != 1.55 {
		t.Fatalf("got %+v", bars)
	}
}

func TestMarketIDOption(t *testing.T) {
	m, err := New(Options{Symbols: []string{"SOLUSDT"}, Interval: "1s", ID: "binance-spot:SOLUSDT"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := m.Descriptor(); d.ID != "binance-spot:SOLUSDT" || d.Granularity.Duration() != time.Second {
		t.Fatalf("descriptor %+v", d)
	}
	if m, _ := New(Options{Symbols: []string{"X"}}, nil); m.Descriptor().ID != "binance-spot" {
		t.Fatal("default id changed")
	}
}

func TestLogo(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logos/SOL.png":
			w.Write(png)
		case "/logos/HTML.png":
			w.Write([]byte("<html>"))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	p := &Public{LogoURL: srv.URL + "/logos/", HTTP: srv.Client()}
	if raw, err := p.Logo(context.Background(), "SOL"); err != nil || string(raw) != string(png) {
		t.Fatalf("%q %v", raw, err)
	}
	for _, base := range []string{"NOPE", "HTML", "../x", "sol", ""} {
		if _, err := p.Logo(context.Background(), base); err == nil {
			t.Errorf("%q: expected an error", base)
		}
	}
	if !ValidTicker("1000SATS") || ValidTicker("AbCd…WxYz") {
		t.Fatal("ValidTicker")
	}
}
