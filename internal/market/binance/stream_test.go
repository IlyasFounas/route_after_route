package binance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

func klineMsg(sym string, openMs int64, close string, closed bool) string {
	// "T" (close time) and "L" (last trade id) are present on purpose: they
	// must not overwrite "t" and "l".
	return fmt.Sprintf(`{"stream":"%s@kline_1m","data":{"e":"kline","E":1,"s":"%s","k":{"t":%d,"T":%d,"s":"%s","i":"1m","o":"10","c":"%s","h":"12","l":"9","L":999999,"v":"5","x":%t}}}`,
		strings.ToLower(sym), sym, openMs, openMs+59999, sym, close, closed)
}

func TestStreamParsesAndReconnects(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("streams"); got != "btcusdt@kline_1m" {
			t.Errorf("streams = %q", got)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		n := conns.Add(1)
		ctx := r.Context()
		c.Write(ctx, websocket.MessageText, []byte(`{"result":null,"id":1}`))
		base := int64(1700000000000) + int64(n-1)*60000
		c.Write(ctx, websocket.MessageText, []byte(klineMsg("BTCUSDT", base, "11", false)))
		c.Write(ctx, websocket.MessageText, []byte(klineMsg("BTCUSDT", base, "11.5", true)))
		if n == 1 {
			return // drop the first connection: the client must reconnect
		}
		<-ctx.Done()
	}))
	defer srv.Close()

	m, err := New(Options{Symbols: []string{"BTCUSDT"}, Interval: market.Minute, StreamURL: "ws" + strings.TrimPrefix(srv.URL, "http")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.backoff = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(chan market.Update, 16)
	var states []string
	done := make(chan error, 1)
	go func() {
		done <- m.Stream(ctx, []string{"BTCUSDT"}, out, func(s market.StreamStatus) { states = append(states, s.State) })
	}()

	var got []market.Update
	for len(got) < 4 {
		select {
		case u := <-out:
			got = append(got, u)
		case <-ctx.Done():
			t.Fatalf("timeout, got %d updates", len(got))
		}
	}
	cancel()
	<-done

	first := got[0]
	if first.Closed || first.Bar.Close != 11 || first.Bar.Low != 9 || first.Market != "binance-spot" || first.Symbol != "BTCUSDT" {
		t.Fatalf("first update = %+v", first)
	}
	if !first.Bar.Time.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Fatalf("open time overwritten by close time: %v", first.Bar.Time)
	}
	if !got[1].Closed || got[1].Bar.Close != 11.5 {
		t.Fatalf("second update = %+v", got[1])
	}
	if !got[2].Bar.Time.After(got[1].Bar.Time) {
		t.Fatal("updates after reconnection should continue in time")
	}
	if conns.Load() < 2 {
		t.Fatal("expected a reconnection")
	}
	joined := strings.Join(states, ",")
	if !strings.Contains(joined, market.StreamReconnecting) || !strings.Contains(joined, market.StreamConnected) {
		t.Fatalf("status sequence = %s", joined)
	}
}
