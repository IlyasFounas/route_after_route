// Package app is the composition root: it wires concrete markets and
// strategies into the 7-layer pipeline. It is the only internal package
// allowed to know about everything.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/arbiter"
	"github.com/IlyasFounas/route_after_route/internal/backtest"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

// Config is the run configuration (config.json). Fields absent from the file
// keep the values of DefaultConfig.
type Config struct {
	Markets         []MarketConfig   `json:"markets"`
	Strategies      []StrategyConfig `json:"strategies"`
	Period          PeriodConfig     `json:"period"`
	Backtest        BacktestConfig   `json:"backtest"`
	Policy          PolicyConfig     `json:"policy"`
	ReevaluateEvery Duration         `json:"reevaluate_every"`

	baseDir string
}

// MarketConfig selects one market adapter.
type MarketConfig struct {
	Type    string          `json:"type"`
	Options json.RawMessage `json:"options"`
}

// StrategyConfig instantiates one catalogue strategy. Name defaults to ID and
// must be unique (the same strategy may appear with different parameters).
type StrategyConfig struct {
	ID     string             `json:"id"`
	Name   string             `json:"name,omitempty"`
	Params map[string]float64 `json:"params,omitempty"`
}

// PeriodConfig is the evaluated history window. Dates are YYYY-MM-DD or
// RFC 3339. "to" defaults to now; give either "from" or "lookback_days".
type PeriodConfig struct {
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	LookbackDays int    `json:"lookback_days,omitempty"`
}

// BacktestConfig holds the user-tunable execution assumptions.
type BacktestConfig struct {
	SlippageBps  float64 `json:"slippage_bps"`
	PositionStep float64 `json:"position_step"`
	TrainBars    int     `json:"train_bars"`
	TestBars     int     `json:"test_bars"`
}

// PolicyConfig is the user-tunable part of arbiter.Policy (MaxDrawdown comes
// from the profile).
type PolicyConfig struct {
	MaxActive int          `json:"max_active"`
	Rule      arbiter.Rule `json:"rule"`
	Cap       float64      `json:"cap"`
	MinScore  float64      `json:"min_score"`
}

// Duration is a time.Duration encoded as a Go duration string ("24h").
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"24h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Period:          PeriodConfig{LookbackDays: 730},
		Backtest:        BacktestConfig{SlippageBps: 5, PositionStep: 0.25, TrainBars: 250, TestBars: 60},
		Policy:          PolicyConfig{MaxActive: 3, Rule: arbiter.RuleCapped, Cap: 0.5, MinScore: 0},
		ReevaluateEvery: Duration{24 * time.Hour},
		baseDir:         ".",
	}
}

// LoadConfig reads a config file strictly over the defaults.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	// "from" and "lookback_days" are exclusive: a file setting "from" must
	// not inherit the default lookback.
	var probe struct {
		Period map[string]json.RawMessage `json:"period"`
	}
	_ = json.Unmarshal(raw, &probe)
	if _, ok := probe.Period["from"]; ok {
		cfg.Period.LookbackDays = 0
	}
	if err := market.DecodeStrict(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.baseDir = filepath.Dir(path)
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the configuration.
func (c Config) Validate() error {
	var errs []error
	if len(c.Markets) == 0 {
		errs = append(errs, errors.New("at least one market must be selected"))
	}
	if c.Period.From != "" && c.Period.LookbackDays > 0 {
		errs = append(errs, errors.New("period: give either from or lookback_days, not both"))
	}
	if c.Period.LookbackDays < 0 {
		errs = append(errs, errors.New("period: lookback_days must be >= 0"))
	}
	if _, _, err := c.Period.Resolve(time.Now()); err != nil {
		errs = append(errs, err)
	}
	if err := c.engineConfig().Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.walkForward().Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.policy(profile.Profile{}).Validate(); err != nil {
		errs = append(errs, fmt.Errorf("policy: %w", err))
	}
	if c.ReevaluateEvery.Duration <= 0 {
		errs = append(errs, errors.New("reevaluate_every must be > 0"))
	}
	return errors.Join(errs...)
}

func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD or RFC 3339)", s)
	}
	return t, nil
}

// Resolve returns the [from, to) window.
func (p PeriodConfig) Resolve(now time.Time) (from, to time.Time, err error) {
	to = now.UTC()
	if p.To != "" {
		if to, err = parseDate(p.To); err != nil {
			return
		}
	}
	switch {
	case p.From != "":
		if from, err = parseDate(p.From); err != nil {
			return
		}
	case p.LookbackDays > 0:
		from = to.AddDate(0, 0, -p.LookbackDays)
	default:
		return from, to, errors.New("period: from or lookback_days is required")
	}
	if !from.Before(to) {
		err = fmt.Errorf("period: from (%s) must be before to (%s)", from.Format(time.DateOnly), to.Format(time.DateOnly))
	}
	return
}

func (c Config) engineConfig() backtest.Config {
	return backtest.Config{SlippageBps: c.Backtest.SlippageBps, PositionStep: c.Backtest.PositionStep}
}

func (c Config) walkForward() backtest.WalkForward {
	return backtest.WalkForward{Train: c.Backtest.TrainBars, Test: c.Backtest.TestBars}
}

func (c Config) policy(p profile.Profile) arbiter.Policy {
	return arbiter.Policy{MaxActive: c.Policy.MaxActive, Rule: c.Policy.Rule, Cap: c.Policy.Cap, MinScore: c.Policy.MinScore, MaxDrawdown: p.MaxDrawdown()}
}

// OpenMarkets instantiates the selected market adapters.
func (c Config) OpenMarkets() ([]market.Market, error) {
	var out []market.Market
	seen := map[string]bool{}
	for i, mc := range c.Markets {
		m, err := market.Open(mc.Type, mc.Options, c.baseDir)
		if err != nil {
			return nil, fmt.Errorf("markets[%d]: %w", i, err)
		}
		id := m.Descriptor().ID
		if seen[id] {
			return nil, fmt.Errorf("markets[%d]: duplicate market id %q", i, id)
		}
		seen[id] = true
		out = append(out, m)
	}
	return out, nil
}

// StrategyInstance is a configured strategy with its unique name.
type StrategyInstance struct {
	Name string
	strategy.Strategy
}

// OpenStrategies instantiates the configured strategies (the whole catalogue
// with default parameters if none is configured).
func (c Config) OpenStrategies() ([]StrategyInstance, error) {
	cfgs := c.Strategies
	if len(cfgs) == 0 {
		for _, id := range strategy.IDs() {
			cfgs = append(cfgs, StrategyConfig{ID: id})
		}
	}
	var out []StrategyInstance
	seen := map[string]bool{}
	for i, sc := range cfgs {
		s, err := strategy.New(sc.ID, sc.Params)
		if err != nil {
			return nil, fmt.Errorf("strategies[%d]: %w", i, err)
		}
		name := sc.Name
		if name == "" {
			name = sc.ID
		}
		if seen[name] {
			return nil, fmt.Errorf("strategies[%d]: duplicate name %q (set a distinct \"name\")", i, name)
		}
		seen[name] = true
		out = append(out, StrategyInstance{Name: name, Strategy: s})
	}
	return out, nil
}
