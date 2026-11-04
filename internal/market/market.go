// Package market implements layer 2: the common interface every market module
// must implement.
//
// ARCHITECTURE CONTRACT: layers 3 to 7 only ever see Market, Descriptor and
// Bar. They must never import a concrete adapter (internal/market/<adapter>).
// This rule is enforced by internal/archtest.
package market

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/profile"
)

// Market is the single interface upper layers depend on.
type Market interface {
	// Descriptor returns the static declaration of the market.
	Descriptor() Descriptor
	// Bars returns the history of symbol in [from, to), sorted by strictly
	// increasing Time, at the market's granularity. Only completed bars are
	// returned. Implementations must return bars that pass ValidateBars.
	Bars(ctx context.Context, symbol string, from, to time.Time) ([]Bar, error)
}

// Update is a real-time observation pushed by a Streamer. While Closed is
// false, Bar is the candle still forming (its values change on every
// update); the final update of a candle has Closed set to true.
type Update struct {
	Market string
	Symbol string
	Bar    Bar
	Closed bool
}

// Streamer is an OPTIONAL interface a Market may implement to push real-time
// updates. Callers detect it with a type assertion; markets without it are
// polled through Bars.
type Streamer interface {
	// Stream sends updates for symbols to out until ctx is cancelled or a
	// non-recoverable error occurs. Implementations reconnect on their own
	// after transient failures and report status changes on status (which
	// may be nil). Stream must not close out.
	Stream(ctx context.Context, symbols []string, out chan<- Update, status func(StreamStatus)) error
}

// StreamStatus describes the health of a stream.
type StreamStatus struct {
	State string // "connecting", "connected", "reconnecting", "finished"
	Err   error
}

const (
	StreamConnecting   = "connecting"
	StreamConnected    = "connected"
	StreamReconnecting = "reconnecting"
	StreamFinished     = "finished"
)

// Bar is one OHLCV observation. Time is the bar's opening timestamp (UTC).
type Bar struct {
	Time   time.Time `json:"time"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

// Granularity is the time step of a market's bars.
type Granularity string

const (
	Second         Granularity = "1s"
	Minute         Granularity = "1m"
	FiveMinutes    Granularity = "5m"
	FifteenMinutes Granularity = "15m"
	Hour           Granularity = "1h"
	FourHours      Granularity = "4h"
	Day            Granularity = "1d"
	Week           Granularity = "1w"
)

var granularities = map[Granularity]time.Duration{
	Second:         time.Second,
	Minute:         time.Minute,
	FiveMinutes:    5 * time.Minute,
	FifteenMinutes: 15 * time.Minute,
	Hour:           time.Hour,
	FourHours:      4 * time.Hour,
	Day:            24 * time.Hour,
	Week:           7 * 24 * time.Hour,
}

func (g Granularity) Valid() bool { _, ok := granularities[g]; return ok }

// Duration returns the length of one bar (0 if g is invalid).
func (g Granularity) Duration() time.Duration { return granularities[g] }

// Kind is the asset class, used for strategy prerequisites and annualisation.
type Kind string

const (
	KindCrypto Kind = "crypto"
	KindEquity Kind = "equity"
	KindFX     Kind = "fx"
)

func (k Kind) Valid() bool { return k == KindCrypto || k == KindEquity || k == KindFX }

// Fees are expressed in basis points of traded notional.
type Fees struct {
	TakerBps float64 `json:"taker_bps"`
	MakerBps float64 `json:"maker_bps"`
}

// Access describes the constraints a user must satisfy to use the market.
type Access struct {
	ShortAllowed    bool            `json:"short_allowed"`
	RequiresAccount bool            `json:"requires_account"`
	RequiredSkills  []profile.Skill `json:"required_skills,omitempty"`
	MinHoursPerWeek int             `json:"min_hours_per_week"`
}

// Symbol is a tradable instrument. Delisted symbols MUST stay declared so the
// backtester can include them (survivorship-bias guard).
type Symbol struct {
	Name     string     `json:"name"`
	Listed   time.Time  `json:"listed,omitzero"`
	Delisted *time.Time `json:"delisted,omitempty"`
}

// ActiveAt reports whether the symbol was tradable at t.
func (s Symbol) ActiveAt(t time.Time) bool {
	if !s.Listed.IsZero() && t.Before(s.Listed) {
		return false
	}
	return s.Delisted == nil || t.Before(*s.Delisted)
}

// OverlapsPeriod reports whether the symbol was tradable at some point in [from, to).
func (s Symbol) OverlapsPeriod(from, to time.Time) bool {
	return (s.Listed.IsZero() || s.Listed.Before(to)) && (s.Delisted == nil || s.Delisted.After(from))
}

// Descriptor is the static declaration of a market module.
type Descriptor struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Kind        Kind        `json:"kind"`
	Granularity Granularity `json:"granularity"`
	Fees        Fees        `json:"fees"`
	// MinCapital in minor currency units.
	MinCapital int64    `json:"min_capital"`
	Access     Access   `json:"access"`
	Symbols    []Symbol `json:"symbols"`
	DataSource string   `json:"data_source"`
}

// Validate checks the declaration is complete and coherent.
func (d Descriptor) Validate() error {
	var errs []error
	if d.ID == "" {
		errs = append(errs, errors.New("id is required"))
	}
	if !d.Kind.Valid() {
		errs = append(errs, fmt.Errorf("invalid kind %q", d.Kind))
	}
	if !d.Granularity.Valid() {
		errs = append(errs, fmt.Errorf("invalid granularity %q", d.Granularity))
	}
	if d.Fees.TakerBps < 0 || d.Fees.MakerBps < 0 {
		errs = append(errs, errors.New("fees must be >= 0"))
	}
	if d.MinCapital < 0 {
		errs = append(errs, errors.New("min_capital must be >= 0"))
	}
	if d.Access.MinHoursPerWeek < 0 {
		errs = append(errs, errors.New("min_hours_per_week must be >= 0"))
	}
	for _, s := range d.Access.RequiredSkills {
		if !s.Valid() {
			errs = append(errs, fmt.Errorf("unknown required skill %q", s))
		}
	}
	if len(d.Symbols) == 0 {
		errs = append(errs, errors.New("at least one symbol is required"))
	}
	seen := map[string]bool{}
	for _, s := range d.Symbols {
		switch {
		case s.Name == "":
			errs = append(errs, errors.New("symbol with empty name"))
		case seen[s.Name]:
			errs = append(errs, fmt.Errorf("duplicate symbol %q", s.Name))
		}
		seen[s.Name] = true
		if s.Delisted != nil && !s.Listed.IsZero() && !s.Delisted.After(s.Listed) {
			errs = append(errs, fmt.Errorf("symbol %q delisted before being listed", s.Name))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("market %q: %w", d.ID, errors.Join(errs...))
	}
	return nil
}

// HasSymbol reports whether name is declared.
func (d Descriptor) HasSymbol(name string) bool {
	return slices.ContainsFunc(d.Symbols, func(s Symbol) bool { return s.Name == name })
}

// PeriodsPerYear is the annualisation factor for returns at this market's
// granularity: crypto trades 365 days × 24 h, equities 252 sessions × 6.5 h,
// FX 260 days × 24 h.
func (d Descriptor) PeriodsPerYear() float64 {
	dur := d.Granularity.Duration()
	if dur <= 0 {
		return 0
	}
	days, sessionHours := 365.0, 24.0
	switch d.Kind {
	case KindEquity:
		days, sessionHours = 252, 6.5
	case KindFX:
		days = 260
	}
	switch {
	case dur >= 7*24*time.Hour:
		return 52 * float64(7*24*time.Hour) / float64(dur)
	case dur >= 24*time.Hour:
		return days * float64(24*time.Hour) / float64(dur)
	default:
		return days * sessionHours / dur.Hours()
	}
}

// ValidateBars enforces the Bars contract.
func ValidateBars(bars []Bar) error {
	for i, b := range bars {
		if i > 0 && !b.Time.After(bars[i-1].Time) {
			return fmt.Errorf("bar %d (%s): timestamps must be strictly increasing", i, b.Time.Format(time.RFC3339))
		}
		for _, v := range [...]float64{b.Open, b.High, b.Low, b.Close} {
			if !(v > 0) || math.IsInf(v, 0) {
				return fmt.Errorf("bar %d (%s): prices must be positive and finite", i, b.Time.Format(time.RFC3339))
			}
		}
		if !(b.Volume >= 0) || math.IsInf(b.Volume, 0) {
			return fmt.Errorf("bar %d (%s): volume must be >= 0 and finite", i, b.Time.Format(time.RFC3339))
		}
		if b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) {
			return fmt.Errorf("bar %d (%s): inconsistent OHLC", i, b.Time.Format(time.RFC3339))
		}
	}
	return nil
}

// Compatible returns the reasons why the profile cannot use the market
// (empty means compatible).
func Compatible(d Descriptor, p profile.Profile) []string {
	var r []string
	if p.Capital < d.MinCapital {
		r = append(r, fmt.Sprintf("capital %d below market minimum %d", p.Capital, d.MinCapital))
	}
	if p.HoursPerWeek < d.Access.MinHoursPerWeek {
		r = append(r, fmt.Sprintf("hours_per_week %d below market minimum %d", p.HoursPerWeek, d.Access.MinHoursPerWeek))
	}
	for _, s := range d.Access.RequiredSkills {
		if !p.HasSkill(s) {
			r = append(r, fmt.Sprintf("missing required skill %q", s))
		}
	}
	return r
}

// Rejection records why a market was filtered out.
type Rejection struct {
	Market  string   `json:"market"`
	Reasons []string `json:"reasons"`
}

// Select splits the user-selected markets into compatible and rejected ones.
func Select(ms []Market, p profile.Profile) (accepted []Market, rejected []Rejection) {
	for _, m := range ms {
		d := m.Descriptor()
		if r := Compatible(d, p); len(r) > 0 {
			rejected = append(rejected, Rejection{Market: d.ID, Reasons: r})
			continue
		}
		accepted = append(accepted, m)
	}
	return accepted, rejected
}
