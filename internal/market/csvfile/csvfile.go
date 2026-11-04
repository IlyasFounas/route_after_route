// Package csvfile is an offline market adapter reading local files:
//
//	<dir>/market.json    the market Descriptor
//	<dir>/<SYMBOL>.csv   header "time,open,high,low,close,volume"; time is
//	                     RFC 3339 or YYYY-MM-DD (UTC)
//
// It is deterministic and therefore the reference adapter for tests and
// backtests.
package csvfile

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
)

// Options is the JSON configuration of the adapter.
type Options struct {
	Path string `json:"path"`
}

func init() {
	market.Register("csvfile", func(raw json.RawMessage, baseDir string) (market.Market, error) {
		var o Options
		if err := market.DecodeStrict(raw, &o); err != nil {
			return nil, err
		}
		if o.Path == "" {
			return nil, errors.New("options.path is required")
		}
		p := o.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		return New(p)
	})
}

// Market reads bars from CSV files.
type Market struct {
	dir  string
	desc market.Descriptor
}

var _ market.Market = (*Market)(nil)

// New loads <dir>/market.json.
func New(dir string) (*Market, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "market.json"))
	if err != nil {
		return nil, err
	}
	var d market.Descriptor
	if err := market.DecodeStrict(raw, &d); err != nil {
		return nil, fmt.Errorf("%s/market.json: %w", dir, err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &Market{dir: dir, desc: d}, nil
}

func (m *Market) Descriptor() market.Descriptor { return m.desc }

var header = []string{"time", "open", "high", "low", "close", "volume"}

func (m *Market) Bars(ctx context.Context, symbol string, from, to time.Time) ([]market.Bar, error) {
	if !m.desc.HasSymbol(symbol) {
		return nil, fmt.Errorf("unknown symbol %q", symbol)
	}
	if strings.ContainsAny(symbol, `/\`) || strings.Contains(symbol, "..") {
		return nil, fmt.Errorf("invalid symbol name %q", symbol)
	}
	f, err := os.Open(filepath.Join(m.dir, symbol+".csv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = len(header)
	r.ReuseRecord = true
	h, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s.csv: reading header: %w", symbol, err)
	}
	if !slices.Equal(h, header) {
		return nil, fmt.Errorf("%s.csv: header must be %v, got %v", symbol, header, h)
	}
	var bars []market.Bar
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s.csv: %w", symbol, err)
		}
		if line%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		b, err := parseRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("%s.csv line %d: %w", symbol, line, err)
		}
		if b.Time.Before(from) || !b.Time.Before(to) {
			continue
		}
		bars = append(bars, b)
	}
	if err := market.ValidateBars(bars); err != nil {
		return nil, fmt.Errorf("%s.csv: %w", symbol, err)
	}
	return bars, nil
}

func parseRecord(rec []string) (market.Bar, error) {
	t, err := time.Parse(time.RFC3339, rec[0])
	if err != nil {
		t, err = time.Parse(time.DateOnly, rec[0])
		if err != nil {
			return market.Bar{}, fmt.Errorf("invalid time %q", rec[0])
		}
	}
	var v [5]float64
	for i := range v {
		v[i], err = strconv.ParseFloat(rec[i+1], 64)
		if err != nil {
			return market.Bar{}, fmt.Errorf("invalid %s %q", header[i+1], rec[i+1])
		}
	}
	return market.Bar{Time: t.UTC(), Open: v[0], High: v[1], Low: v[2], Close: v[3], Volume: v[4]}, nil
}
