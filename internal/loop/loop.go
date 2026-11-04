// Package loop implements layer 7: portfolio state persisted between cycles,
// exit conditions, and re-injection of the portfolio value into the profile
// (layer 1) at the next cycle.
package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
)

// Position is an open position recorded by a previous cycle.
type Position struct {
	Market     string    `json:"market"`
	Strategy   string    `json:"strategy"`
	Symbol     string    `json:"symbol"`
	Direction  int       `json:"direction"` // +1 long, -1 short
	Amount     int64     `json:"amount"`    // capital committed at entry (minor units)
	EntryPrice float64   `json:"entry_price"`
	OpenedAt   time.Time `json:"opened_at"`
}

// PnL is the unrealised return of the position at price.
func (p Position) PnL(price float64) float64 {
	return float64(p.Direction) * (price/p.EntryPrice - 1)
}

// Value is the marked-to-market value of the position (never negative).
func (p Position) Value(price float64) int64 {
	return max(0, int64(math.Round(float64(p.Amount)*(1+p.PnL(price)))))
}

// State is the portfolio between cycles. Cycle 0 means "never run".
type State struct {
	Cycle     int        `json:"cycle"`
	UpdatedAt time.Time  `json:"updated_at"`
	Cash      int64      `json:"cash"`
	Positions []Position `json:"positions"`
}

// PriceKey identifies a symbol on a market.
type PriceKey struct{ Market, Symbol string }

// Prices holds the latest close per symbol.
type Prices map[PriceKey]float64

func (ps Prices) of(p Position) (float64, error) {
	v, ok := ps[PriceKey{p.Market, p.Symbol}]
	if !ok {
		return 0, fmt.Errorf("loop: no current price for held position %s/%s", p.Market, p.Symbol)
	}
	return v, nil
}

// Load reads the state file. A missing file yields an empty state.
func Load(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("loop: %s: %w", path, err)
	}
	return s, nil
}

// Save writes the state atomically (temp file + rename).
func Save(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Due reports whether a new cycle should run, and when the next one is due.
func Due(s State, every time.Duration, now time.Time) (bool, time.Time) {
	if s.Cycle == 0 {
		return true, now
	}
	next := s.UpdatedAt.Add(every)
	return !now.Before(next), next
}

// Equity is cash plus the marked-to-market value of every position.
func (s State) Equity(prices Prices) (int64, error) {
	eq := s.Cash
	for _, p := range s.Positions {
		px, err := prices.of(p)
		if err != nil {
			return 0, err
		}
		eq += p.Value(px)
	}
	return eq, nil
}

// NextProfile re-injects the portfolio into layer 1: the capital of the
// next cycle is the current equity. The first cycle uses the profile as is.
func NextProfile(p profile.Profile, s State, prices Prices) (profile.Profile, error) {
	if s.Cycle == 0 {
		return p, nil
	}
	eq, err := s.Equity(prices)
	if err != nil {
		return profile.Profile{}, err
	}
	p.Capital = eq
	if err := p.Validate(); err != nil {
		return profile.Profile{}, fmt.Errorf("loop: portfolio no longer forms a valid profile: %w", err)
	}
	return p, nil
}

// Rules are the exit conditions.
type Rules struct {
	// StopLoss closes a position whose loss reaches this fraction.
	StopLoss float64 `json:"stop_loss"`
}

// RulesFor derives exit rules from the profile: stop-loss at half the
// maximum acceptable drawdown.
func RulesFor(p profile.Profile) Rules { return Rules{StopLoss: p.MaxDrawdown() / 2} }

type posKey struct{ Market, Strategy, Symbol string }

// Advance applies a cycle's actions to the portfolio and returns the new
// state and the orders to carry out.
//
// Exit conditions, checked in order: stop-loss; (market, strategy) no longer
// selected; signal direction changed. Kept positions are not resized.
// New positions are funded from cash only, in rank order.
func Advance(prev State, initialCash int64, actions []output.Action, prices Prices, rules Rules, now time.Time) (State, []output.Order, error) {
	next := State{Cycle: prev.Cycle + 1, UpdatedAt: now, Cash: prev.Cash}
	if prev.Cycle == 0 {
		next.Cash = initialCash
	}
	selected := map[[2]string]bool{}
	signals := map[posKey]output.SymbolSignal{}
	for _, a := range actions {
		selected[[2]string{a.Market, a.Strategy}] = true
		for _, s := range a.Signals {
			signals[posKey{a.Market, a.Strategy, s.Symbol}] = s
		}
	}

	var orders []output.Order
	held := map[posKey]bool{}
	stopped := map[posKey]bool{}
	for _, p := range prev.Positions {
		px, err := prices.of(p)
		if err != nil {
			return State{}, nil, err
		}
		k := posKey{p.Market, p.Strategy, p.Symbol}
		pnl := p.PnL(px)
		sig, hasSig := signals[k]
		reason := ""
		switch {
		case pnl <= -rules.StopLoss:
			reason = fmt.Sprintf("stop-loss: %.2f%% <= -%.2f%%", pnl*100, rules.StopLoss*100)
			stopped[k] = true
		case !selected[[2]string{p.Market, p.Strategy}]:
			reason = "strategy no longer selected by arbitration"
		case !hasSig || output.Sign(sig.Direction) != p.Direction:
			reason = "signal direction changed"
		}
		dir := output.DirectionOf(float64(p.Direction))
		if reason != "" {
			v := p.Value(px)
			next.Cash += v
			orders = append(orders, output.Order{Kind: output.OrderClose, Market: p.Market, Strategy: p.Strategy, Symbol: p.Symbol, Direction: dir, Amount: v, Price: px, PnL: pnl, Reason: reason})
			continue
		}
		held[k] = true
		next.Positions = append(next.Positions, p)
		orders = append(orders, output.Order{Kind: output.OrderHold, Market: p.Market, Strategy: p.Strategy, Symbol: p.Symbol, Direction: dir, Amount: p.Value(px), Price: px, PnL: pnl, Reason: "signal unchanged"})
	}

	for _, a := range actions {
		for _, s := range a.Signals {
			k := posKey{a.Market, a.Strategy, s.Symbol}
			if s.Allocation <= 0 || s.Direction == output.Flat || held[k] || stopped[k] {
				continue
			}
			amt := min(s.Allocation, next.Cash)
			if amt <= 0 {
				continue
			}
			next.Cash -= amt
			p := Position{Market: a.Market, Strategy: a.Strategy, Symbol: s.Symbol, Direction: output.Sign(s.Direction), Amount: amt, EntryPrice: s.Price, OpenedAt: now}
			next.Positions = append(next.Positions, p)
			orders = append(orders, output.Order{Kind: output.OrderOpen, Market: a.Market, Strategy: a.Strategy, Symbol: s.Symbol, Direction: s.Direction, Amount: amt, Price: s.Price,
				Reason: fmt.Sprintf("rank %d, signal %+.3f, exposure %+.2f", a.Rank, s.Signal, s.Exposure)})
		}
	}
	return next, orders, nil
}
