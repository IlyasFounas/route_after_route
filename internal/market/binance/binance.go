// Package binance is a market adapter for Binance spot public market data
// (klines endpoint, no API key). It only reads data; it never places orders.
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

const (
	defaultBaseURL = "https://api.binance.com"
	pageLimit      = 1000
)

// Options is the JSON configuration of the adapter.
type Options struct {
	Symbols  []string           `json:"symbols"`
	Interval market.Granularity `json:"interval"`
	BaseURL  string             `json:"base_url,omitempty"`
}

func init() {
	market.Register("binance", func(raw json.RawMessage, _ string) (market.Market, error) {
		var o Options
		if err := market.DecodeStrict(raw, &o); err != nil {
			return nil, err
		}
		return New(o, nil)
	})
}

// Market fetches klines over HTTP.
type Market struct {
	opts   Options
	client *http.Client
	desc   market.Descriptor
	now    func() time.Time
}

var _ market.Market = (*Market)(nil)

// New builds the adapter. A nil client gets a default one with a timeout.
func New(o Options, client *http.Client) (*Market, error) {
	if len(o.Symbols) == 0 {
		return nil, errors.New("options.symbols is required")
	}
	if o.Interval == "" {
		o.Interval = market.Day
	}
	if !o.Interval.Valid() {
		return nil, fmt.Errorf("invalid interval %q", o.Interval)
	}
	if o.BaseURL == "" {
		o.BaseURL = defaultBaseURL
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	syms := make([]market.Symbol, len(o.Symbols))
	for i, s := range o.Symbols {
		syms[i] = market.Symbol{Name: s}
	}
	return &Market{
		opts:   o,
		client: client,
		now:    time.Now,
		desc: market.Descriptor{
			ID:          "binance-spot",
			Name:        "Binance Spot (public market data)",
			Kind:        market.KindCrypto,
			Granularity: o.Interval,
			Fees:        market.Fees{TakerBps: 10, MakerBps: 10},
			MinCapital:  1000, // 10.00 in quote currency minor units
			Access:      market.Access{ShortAllowed: false, RequiresAccount: true},
			Symbols:     syms,
			DataSource:  o.BaseURL + "/api/v3/klines",
		},
	}, nil
}

func (m *Market) Descriptor() market.Descriptor { return m.desc }

func (m *Market) Bars(ctx context.Context, symbol string, from, to time.Time) ([]market.Bar, error) {
	if !m.desc.HasSymbol(symbol) {
		return nil, fmt.Errorf("unknown symbol %q", symbol)
	}
	nowMs := m.now().UnixMilli()
	start, end := from.UnixMilli(), to.UnixMilli()-1
	var bars []market.Bar
	for start <= end {
		rows, err := m.fetch(ctx, symbol, start, end)
		if err != nil {
			return nil, err
		}
		var lastOpen int64 = -1
		for _, row := range rows {
			b, openMs, closeMs, err := parseKline(row)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", symbol, err)
			}
			lastOpen = openMs
			if closeMs > nowMs { // candle still forming
				continue
			}
			bars = append(bars, b)
		}
		if len(rows) < pageLimit || lastOpen < start {
			break
		}
		start = lastOpen + 1
	}
	if err := market.ValidateBars(bars); err != nil {
		return nil, fmt.Errorf("%s: %w", symbol, err)
	}
	return bars, nil
}

func (m *Market) fetch(ctx context.Context, symbol string, start, end int64) ([][]any, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", string(m.opts.Interval))
	q.Set("startTime", strconv.FormatInt(start, 10))
	q.Set("endTime", strconv.FormatInt(end, 10))
	q.Set("limit", strconv.Itoa(pageLimit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.opts.BaseURL+"/api/v3/klines?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("binance %s: HTTP %d: %s", symbol, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var rows [][]any
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("binance %s: decoding klines: %w", symbol, err)
	}
	return rows, nil
}

// parseKline decodes [openTime, open, high, low, close, volume, closeTime, ...].
func parseKline(row []any) (market.Bar, int64, int64, error) {
	if len(row) < 7 {
		return market.Bar{}, 0, 0, fmt.Errorf("kline has %d fields, want >= 7", len(row))
	}
	openMs, err := toInt(row[0])
	if err != nil {
		return market.Bar{}, 0, 0, err
	}
	closeMs, err := toInt(row[6])
	if err != nil {
		return market.Bar{}, 0, 0, err
	}
	var v [5]float64
	for i := range v {
		if v[i], err = toFloat(row[i+1]); err != nil {
			return market.Bar{}, 0, 0, err
		}
	}
	b := market.Bar{Time: time.UnixMilli(openMs).UTC(), Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4]}
	return b, openMs, closeMs, nil
}

func toInt(v any) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
	return n.Int64()
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case string:
		return strconv.ParseFloat(x, 64)
	case json.Number:
		return x.Float64()
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}
