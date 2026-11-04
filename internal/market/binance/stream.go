package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

const maxBackoff = time.Minute

// Stream subscribes to the combined kline stream of symbols and reconnects
// with exponential backoff until ctx is cancelled. Binance closes every
// connection after 24 h; that is handled as an ordinary reconnection.
func (m *Market) Stream(ctx context.Context, symbols []string, out chan<- market.Update, status func(market.StreamStatus)) error {
	report := func(state string, err error) {
		if status != nil {
			status(market.StreamStatus{State: state, Err: err})
		}
	}
	for _, s := range symbols {
		if !m.desc.HasSymbol(s) {
			return fmt.Errorf("unknown symbol %q", s)
		}
	}
	backoff := m.backoff
	report(market.StreamConnecting, nil)
	for {
		started := time.Now()
		err := m.streamOnce(ctx, symbols, out, func() { report(market.StreamConnected, nil) })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > maxBackoff {
			backoff = m.backoff // the connection was healthy: start over
		}
		report(market.StreamReconnecting, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

func (m *Market) streamOnce(ctx context.Context, symbols []string, out chan<- market.Update, connected func()) error {
	names := make([]string, len(symbols))
	for i, s := range symbols {
		names[i] = strings.ToLower(s) + "@kline_" + string(m.opts.Interval)
	}
	c, _, err := websocket.Dial(ctx, m.opts.StreamURL+"/stream?streams="+strings.Join(names, "/"), nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	connected()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		u, ok, err := parseStreamKline(data)
		if err != nil {
			return fmt.Errorf("binance stream: %w", err)
		}
		if !ok {
			continue
		}
		u.Market = m.desc.ID
		select {
		case out <- u:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// parseStreamKline decodes {"stream": "...", "data": {"e": "kline", "k": {...}}}.
// Keys are read exactly: encoding/json matches struct fields
// case-insensitively, and Binance uses both "t"/"T" and "l"/"L".
func parseStreamKline(data []byte) (market.Update, bool, error) {
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return market.Update{}, false, err
	}
	var event string
	if raw, ok := env.Data["e"]; !ok || json.Unmarshal(raw, &event) != nil || event != "kline" {
		return market.Update{}, false, nil // not a kline event (e.g. subscription ack)
	}
	var k map[string]json.RawMessage
	if err := json.Unmarshal(env.Data["k"], &k); err != nil {
		return market.Update{}, false, fmt.Errorf("kline payload: %w", err)
	}
	var (
		openMs int64
		symbol string
		closed bool
		v      [5]float64
	)
	if err := json.Unmarshal(k["t"], &openMs); err != nil {
		return market.Update{}, false, fmt.Errorf("kline t: %w", err)
	}
	if err := json.Unmarshal(k["s"], &symbol); err != nil {
		return market.Update{}, false, fmt.Errorf("kline s: %w", err)
	}
	if err := json.Unmarshal(k["x"], &closed); err != nil {
		return market.Update{}, false, fmt.Errorf("kline x: %w", err)
	}
	for i, key := range [...]string{"o", "h", "l", "c", "v"} {
		var str string
		if err := json.Unmarshal(k[key], &str); err != nil {
			return market.Update{}, false, fmt.Errorf("kline %s: %w", key, err)
		}
		f, err := toFloat(str)
		if err != nil {
			return market.Update{}, false, fmt.Errorf("kline %s: %w", key, err)
		}
		v[i] = f
	}
	bar := market.Bar{Time: time.UnixMilli(openMs).UTC(), Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4]}
	if err := market.ValidateBars([]market.Bar{bar}); err != nil {
		return market.Update{}, false, err
	}
	return market.Update{Symbol: symbol, Bar: bar, Closed: closed}, true, nil
}
