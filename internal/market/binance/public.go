package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

// Public is a client for Binance public endpoints other than klines.
type Public struct {
	BaseURL string
	// LogoURL is where Binance serves the asset logos, as <LogoURL>/<TICKER>.png.
	LogoURL string
	HTTP    *http.Client
}

const defaultLogoURL = "https://bin.bnbstatic.com/static/assets/logos"

// maxLogoBytes bounds a downloaded logo.
const maxLogoBytes = 512 << 10

// NewPublic returns a client on the default endpoint.
func NewPublic() *Public {
	return &Public{BaseURL: defaultBaseURL, LogoURL: defaultLogoURL, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// ValidTicker reports whether base looks like a Binance asset ticker (e.g.
// SOL, 1000SATS): only those are asked for a logo.
func ValidTicker(base string) bool {
	if base == "" || len(base) > 16 {
		return false
	}
	for _, c := range base {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Logo returns the PNG logo of an asset (e.g. SOL), as served by Binance.
func (p *Public) Logo(ctx context.Context, base string) ([]byte, error) {
	if !ValidTicker(base) {
		return nil, fmt.Errorf("binance: invalid ticker %q", base)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.LogoURL, "/")+"/"+base+".png", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance: no logo for %s (HTTP %d)", base, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLogoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxLogoBytes || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, fmt.Errorf("binance: the logo of %s is not a PNG image", base)
	}
	return raw, nil
}

// Ticker is the 24 h summary of a USDT pair.
type Ticker struct {
	Symbol      string  // e.g. SOLUSDT
	Base        string  // e.g. SOL
	Last        float64 // last price in USDT
	Change      float64 // change over 24 h, as a fraction
	QuoteVolume float64 // 24 h volume in USDT
}

// notCrypto lists USDT pairs that are not cryptocurrencies to follow:
// stablecoins and fiat currencies.
var notCrypto = map[string]bool{
	"USDC": true, "FDUSD": true, "TUSD": true, "BUSD": true, "DAI": true, "USDP": true, "USDE": true,
	"PYUSD": true, "USD1": true, "RLUSD": true, "BFUSD": true, "XUSD": true, "USDS": true, "UST": true,
	"EUR": true, "EURI": true, "AEUR": true, "GBP": true, "TRY": true, "BRL": true, "AUD": true,
	"JPY": true, "RUB": true, "UAH": true, "ZAR": true, "PLN": true, "ARS": true, "MXN": true, "COP": true,
}

// USDTTickers lists the actively traded crypto/USDT pairs, most traded
// first. Leveraged tokens, stablecoins, fiat and pairs without trading in the
// last 24 h are left out.
func (p *Public) USDTTickers(ctx context.Context) ([]Ticker, error) {
	var rows []struct {
		Symbol      string `json:"symbol"`
		OpenPrice   string `json:"openPrice"`
		LastPrice   string `json:"lastPrice"`
		QuoteVolume string `json:"quoteVolume"`
		CloseTime   int64  `json:"closeTime"`
	}
	if err := p.get(ctx, "/api/v3/ticker/24hr?type=MINI", &rows); err != nil {
		return nil, err
	}
	staleBefore := time.Now().Add(-24 * time.Hour).UnixMilli()
	var out []Ticker
	for _, r := range rows {
		base, ok := strings.CutSuffix(r.Symbol, "USDT")
		if !ok || base == "" || notCrypto[base] || r.CloseTime < staleBefore {
			continue
		}
		if strings.HasSuffix(base, "UP") && len(base) > 2 || strings.HasSuffix(base, "DOWN") ||
			strings.HasSuffix(base, "BULL") || strings.HasSuffix(base, "BEAR") {
			continue
		}
		last, err1 := strconv.ParseFloat(r.LastPrice, 64)
		open, err2 := strconv.ParseFloat(r.OpenPrice, 64)
		vol, err3 := strconv.ParseFloat(r.QuoteVolume, 64)
		if err1 != nil || err2 != nil || err3 != nil || last <= 0 || vol <= 0 {
			continue
		}
		chg := 0.0
		if open > 0 {
			chg = last/open - 1
		}
		out = append(out, Ticker{Symbol: r.Symbol, Base: base, Last: last, Change: chg, QuoteVolume: vol})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].QuoteVolume > out[j].QuoteVolume })
	return out, nil
}

// Price returns the last price of symbol (e.g. SOLUSDT).
func (p *Public) Price(ctx context.Context, symbol string) (float64, error) {
	var r struct {
		Price string `json:"price"`
	}
	if err := p.get(ctx, "/api/v3/ticker/price?symbol="+url.QueryEscape(symbol), &r); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(r.Price, 64)
}

// Klines returns the last limit candles of symbol at interval (e.g. "5m"),
// the forming one included, oldest first. It is meant for display.
func (p *Public) Klines(ctx context.Context, symbol string, interval market.Granularity, limit int) ([]market.Bar, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", string(interval))
	q.Set("limit", strconv.Itoa(limit))
	var rows [][]any
	if err := p.get(ctx, "/api/v3/klines?"+q.Encode(), &rows); err != nil {
		return nil, err
	}
	out := make([]market.Bar, 0, len(rows))
	for _, r := range rows {
		b, _, _, err := parseKline(r)
		if err != nil {
			return nil, fmt.Errorf("binance %s: %w", symbol, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func (p *Public) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("binance: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber() // klines: integer timestamps must stay exact
	return dec.Decode(v)
}
