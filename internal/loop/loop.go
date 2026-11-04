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
	"slices"
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
	// Cooldown blocks re-entry after a stop-loss: a stopped (market,
	// strategy, symbol) is not reopened in the same direction until its
	// signal has gone flat or reversed.
	Cooldown []Cooldown `json:"cooldown,omitempty"`
	// Trades is the history of closed positions, oldest first. It is only
	// ever appended to (never modified in place), so states may share it.
	Trades []Trade `json:"trades,omitempty"`
}

// Trade is a closed position: what it cost, what it returned.
type Trade struct {
	Market     string    `json:"market"`
	Strategy   string    `json:"strategy"`
	Symbol     string    `json:"symbol"`
	Direction  int       `json:"direction"` // +1 long, -1 short
	Amount     int64     `json:"amount"`    // capital committed at entry (minor units)
	EntryPrice float64   `json:"entry_price"`
	ExitPrice  float64   `json:"exit_price"`
	OpenedAt   time.Time `json:"opened_at"`
	ClosedAt   time.Time `json:"closed_at"`
	// Fees are the trading costs of both sides (minor units), included in
	// PnL.
	Fees int64 `json:"fees,omitempty"`
	// PnL is the net gain (negative: loss), in minor units.
	PnL    int64  `json:"pnl"`
	Reason string `json:"reason"`
}

// Return is the net return of the trade, as a fraction of its amount.
func (t Trade) Return() float64 {
	if t.Amount == 0 {
		return 0
	}
	return float64(t.PnL) / float64(t.Amount)
}

// Held is how long the position was held.
func (t Trade) Held() time.Duration { return t.ClosedAt.Sub(t.OpenedAt) }

// Cooldown is one blocked re-entry.
type Cooldown struct {
	Market    string `json:"market"`
	Strategy  string `json:"strategy"`
	Symbol    string `json:"symbol"`
	Direction int    `json:"direction"`
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

// Rules are the exit conditions and the entry gate.
type Rules struct {
	// StopLoss closes a position whose loss reaches this fraction.
	StopLoss float64 `json:"stop_loss"`
	// MaxHold closes a position held this long (0 = no limit). Like a
	// stop-loss, it blocks re-entry in the same direction until the signal
	// has gone flat or reversed.
	MaxHold time.Duration `json:"max_hold,omitempty"`
	// AllowOpen, when set, gates every new position: an entry it refuses is
	// not opened but returned as an OrderProposal, cash untouched. Nil
	// allows every entry. Exits are never gated.
	AllowOpen func(market, strategy, symbol string, direction int) bool `json:"-"`
	// MaxAmount caps the capital committed to one new position, in minor
	// units (0 = no cap): the bot never puts more than this in one trade.
	MaxAmount int64 `json:"max_amount,omitempty"`
	// CostBps is the trading cost per side (fees + slippage) of each
	// market, in basis points. It is charged when a position closes, for
	// both sides. Missing: no cost (the CLI cycle, whose costs are in the
	// backtest only).
	CostBps map[string]float64 `json:"-"`
}

// close settles p at price px: it returns the cash it gives back (net of
// costs), the close order and the trade for the history.
func (r Rules) close(p Position, px float64, now time.Time, reason string) (int64, output.Order, Trade) {
	v := p.Value(px)
	fees := int64(math.Round((float64(p.Amount) + float64(v)) * r.CostBps[p.Market] / 1e4))
	fees = min(max(fees, 0), v)
	net := v - fees
	t := Trade{Market: p.Market, Strategy: p.Strategy, Symbol: p.Symbol, Direction: p.Direction, Amount: p.Amount,
		EntryPrice: p.EntryPrice, ExitPrice: px, OpenedAt: p.OpenedAt, ClosedAt: now, Fees: fees, PnL: net - p.Amount, Reason: reason}
	o := output.Order{Kind: output.OrderClose, Market: p.Market, Strategy: p.Strategy, Symbol: p.Symbol,
		Direction: output.DirectionOf(float64(p.Direction)), Amount: net, Price: px, PnL: t.Return(), Reason: reason}
	return net, o, t
}

func (r Rules) allowOpen(market, strategy, symbol string, direction int) bool {
	return r.AllowOpen == nil || r.AllowOpen(market, strategy, symbol, direction)
}

// RulesFor derives exit rules from the profile: stop-loss at half the
// maximum acceptable drawdown.
func RulesFor(p profile.Profile) Rules { return Rules{StopLoss: p.MaxDrawdown() / 2} }

type posKey struct{ Market, Strategy, Symbol string }

// stopReason returns a non-empty reason when the position hits the stop-loss
// or has been held for MaxHold.
func stopReason(p Position, pnl float64, rules Rules, now time.Time) string {
	if pnl <= -rules.StopLoss {
		return fmt.Sprintf("stop-loss: %.2f%% <= -%.2f%%", pnl*100, rules.StopLoss*100)
	}
	if rules.MaxHold > 0 && !p.OpenedAt.IsZero() && now.Sub(p.OpenedAt) >= rules.MaxHold {
		return fmt.Sprintf("max hold reached: %s (%+.2f%%)", rules.MaxHold, pnl*100)
	}
	return ""
}

// CloseAll closes every position at the given prices (e.g. when the user
// stops watching a crypto). Positions without a price are closed at their
// entry price.
func CloseAll(prev State, prices Prices, rules Rules, reason string, now time.Time) (State, []output.Order) {
	next := prev
	next.Positions, next.Cooldown = nil, nil
	next.Trades = slices.Clip(prev.Trades)
	var orders []output.Order
	for _, p := range prev.Positions {
		px, ok := prices[PriceKey{p.Market, p.Symbol}]
		if !ok {
			px = p.EntryPrice
		}
		cash, o, t := rules.close(p, px, now, reason)
		next.Cash += cash
		next.Trades = append(next.Trades, t)
		orders = append(orders, o)
	}
	if len(orders) > 0 {
		next.UpdatedAt = now
	}
	return next, orders
}

// CheckStops closes every position whose loss reaches the stop-loss at the
// given prices, or that has been held for MaxHold. The live engine calls it on every price update, between two
// candle closes. Positions without a price are left untouched.
func CheckStops(prev State, prices Prices, rules Rules, now time.Time) (State, []output.Order) {
	var orders []output.Order
	next := prev
	next.Positions = nil
	next.Cooldown = slices.Clone(prev.Cooldown)
	next.Trades = slices.Clip(prev.Trades)
	for _, p := range prev.Positions {
		px, ok := prices[PriceKey{p.Market, p.Symbol}]
		pnl := 0.0
		if ok {
			pnl = p.PnL(px)
		}
		reason := ""
		if ok {
			reason = stopReason(p, pnl, rules, now)
		}
		if reason == "" {
			next.Positions = append(next.Positions, p)
			continue
		}
		cash, o, t := rules.close(p, px, now, reason)
		next.Cash += cash
		next.Trades = append(next.Trades, t)
		next.Cooldown = append(next.Cooldown, Cooldown{p.Market, p.Strategy, p.Symbol, p.Direction})
		orders = append(orders, o)
	}
	if len(orders) > 0 {
		next.UpdatedAt = now
	}
	return next, orders
}

// Advance applies a cycle's actions to the portfolio and returns the new
// state and the orders to carry out.
//
// Exit conditions, checked in order: stop-loss or max hold; (market,
// strategy) no longer selected; signal direction changed. Kept positions are
// not resized. New positions are funded from cash only, in rank order, at
// most rules.MaxAmount each, and only when rules.AllowOpen accepts them
// (otherwise: OrderProposal).
func Advance(prev State, initialCash int64, actions []output.Action, prices Prices, rules Rules, now time.Time) (State, []output.Order, error) {
	next := State{Cycle: prev.Cycle + 1, UpdatedAt: now, Cash: prev.Cash, Trades: slices.Clip(prev.Trades)}
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
	cooldown := map[posKey]int{}
	for _, c := range prev.Cooldown {
		cooldown[posKey{c.Market, c.Strategy, c.Symbol}] = c.Direction
	}
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
		case stopReason(p, pnl, rules, now) != "":
			reason = stopReason(p, pnl, rules, now)
			cooldown[k] = p.Direction
		case !selected[[2]string{p.Market, p.Strategy}]:
			reason = "strategy no longer selected by arbitration"
		case !hasSig || output.Sign(sig.Direction) != p.Direction:
			reason = "signal direction changed"
		}
		dir := output.DirectionOf(float64(p.Direction))
		if reason != "" {
			cash, o, t := rules.close(p, px, now, reason)
			next.Cash += cash
			next.Trades = append(next.Trades, t)
			orders = append(orders, o)
			continue
		}
		held[k] = true
		next.Positions = append(next.Positions, p)
		orders = append(orders, output.Order{Kind: output.OrderHold, Market: p.Market, Strategy: p.Strategy, Symbol: p.Symbol, Direction: dir, Amount: p.Value(px), Price: px, PnL: pnl, Reason: "signal unchanged"})
	}

	for _, a := range actions {
		for _, s := range a.Signals {
			k := posKey{a.Market, a.Strategy, s.Symbol}
			if s.Allocation <= 0 || s.Direction == output.Flat || held[k] {
				continue
			}
			if d, ok := cooldown[k]; ok && d == output.Sign(s.Direction) {
				continue
			}
			amt := min(s.Allocation, next.Cash)
			if rules.MaxAmount > 0 {
				amt = min(amt, rules.MaxAmount)
			}
			if amt <= 0 {
				continue
			}
			if !rules.allowOpen(a.Market, a.Strategy, s.Symbol, output.Sign(s.Direction)) {
				orders = append(orders, output.Order{Kind: output.OrderProposal, Market: a.Market, Strategy: a.Strategy, Symbol: s.Symbol, Direction: s.Direction, Amount: amt, Price: s.Price,
					Reason: fmt.Sprintf("awaiting approval: rank %d, signal %+.3f, exposure %+.2f", a.Rank, s.Signal, s.Exposure)})
				continue
			}
			next.Cash -= amt
			p := Position{Market: a.Market, Strategy: a.Strategy, Symbol: s.Symbol, Direction: output.Sign(s.Direction), Amount: amt, EntryPrice: s.Price, OpenedAt: now}
			next.Positions = append(next.Positions, p)
			orders = append(orders, output.Order{Kind: output.OrderOpen, Market: a.Market, Strategy: a.Strategy, Symbol: s.Symbol, Direction: s.Direction, Amount: amt, Price: s.Price,
				Reason: fmt.Sprintf("rank %d, signal %+.3f, exposure %+.2f", a.Rank, s.Signal, s.Exposure)})
		}
	}
	// A cooldown lasts while the signal keeps the stopped direction.
	for _, a := range actions {
		for _, s := range a.Signals {
			k := posKey{a.Market, a.Strategy, s.Symbol}
			if d, ok := cooldown[k]; ok && d == output.Sign(s.Direction) {
				next.Cooldown = append(next.Cooldown, Cooldown{k.Market, k.Strategy, k.Symbol, d})
			}
		}
	}
	return next, orders, nil
}
