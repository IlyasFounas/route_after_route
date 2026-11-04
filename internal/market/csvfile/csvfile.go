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
	// Replay, when set, turns the files into a simulated real-time feed:
	// Bars only returns candles before Start, and Stream plays the following
	// candles one by one, Interval apart.
	Replay *ReplayOptions `json:"replay,omitempty"`
}

// ReplayOptions configures the replay mode.
type ReplayOptions struct {
	Start    string `json:"start"`    // YYYY-MM-DD or RFC 3339
	Interval string `json:"interval"` // Go duration per candle, e.g. "200ms"; "0s" = as fast as possible
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
		m, err := New(p)
		if err != nil || o.Replay == nil {
			return m, err
		}
		if err := m.SetReplay(*o.Replay); err != nil {
			return nil, fmt.Errorf("options.replay: %w", err)
		}
		return m, nil
	})
}

// Market reads bars from CSV files.
type Market struct {
	dir  string
	desc market.Descriptor

	replay      bool
	replayStart time.Time
	replayEvery time.Duration
}

var (
	_ market.Market   = (*Market)(nil)
	_ market.Streamer = (*Market)(nil)
)

// SetReplay enables the replay mode.
func (m *Market) SetReplay(o ReplayOptions) error {
	start, err := time.Parse(time.RFC3339, o.Start)
	if err != nil {
		if start, err = time.Parse(time.DateOnly, o.Start); err != nil {
			return fmt.Errorf("invalid start %q", o.Start)
		}
	}
	every, err := time.ParseDuration(o.Interval)
	if err != nil || every < 0 {
		return fmt.Errorf("invalid interval %q", o.Interval)
	}
	m.replay, m.replayStart, m.replayEvery = true, start.UTC(), every
	return nil
}

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

// Bars returns the stored candles in [from, to). In replay mode, candles
// from the replay start onwards are "in the future" and never returned.
func (m *Market) Bars(ctx context.Context, symbol string, from, to time.Time) ([]market.Bar, error) {
	if m.replay && m.replayStart.Before(to) {
		to = m.replayStart
	}
	return m.read(ctx, symbol, from, to)
}

func (m *Market) read(ctx context.Context, symbol string, from, to time.Time) ([]market.Bar, error) {
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

// Stream plays the candles from the replay start onwards, in time order
// across symbols. Each candle is sent twice: once forming (halfway between
// open and close), then closed. Without replay mode, there is nothing to
// stream and Stream returns immediately.
func (m *Market) Stream(ctx context.Context, symbols []string, out chan<- market.Update, status func(market.StreamStatus)) error {
	report := func(state string) {
		if status != nil {
			status(market.StreamStatus{State: state})
		}
	}
	if !m.replay {
		report(market.StreamFinished)
		return nil
	}
	future := map[string][]market.Bar{}
	var times []time.Time
	for _, s := range symbols {
		bars, err := m.read(ctx, s, m.replayStart, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		future[s] = bars
		for _, b := range bars {
			times = append(times, b.Time)
		}
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	times = slices.CompactFunc(times, func(a, b time.Time) bool { return a.Equal(b) })

	send := func(u market.Update) error {
		select {
		case out <- u:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	wait := func(d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	report(market.StreamConnected)
	next := map[string]int{}
	for _, t := range times {
		var step []market.Update
		for _, s := range symbols {
			i := next[s]
			if i < len(future[s]) && future[s][i].Time.Equal(t) {
				step = append(step, market.Update{Market: m.desc.ID, Symbol: s, Bar: future[s][i], Closed: true})
				next[s] = i + 1
			}
		}
		for _, u := range step {
			if err := send(forming(u)); err != nil {
				return err
			}
		}
		if err := wait(m.replayEvery / 2); err != nil {
			return err
		}
		for _, u := range step {
			if err := send(u); err != nil {
				return err
			}
		}
		if err := wait(m.replayEvery - m.replayEvery/2); err != nil {
			return err
		}
	}
	report(market.StreamFinished)
	return nil
}

// forming returns the half-built version of a closed candle.
func forming(u market.Update) market.Update {
	b := u.Bar
	mid := (b.Open + b.Close) / 2
	b.Close = mid
	b.High = max(b.Open, mid)
	b.Low = min(b.Open, mid)
	b.Volume /= 2
	u.Bar, u.Closed = b, false
	return u
}
