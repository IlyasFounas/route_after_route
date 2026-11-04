package loop

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
)

var t0 = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func action(sym, dir string, alloc int64, price float64) output.Action {
	return output.Action{Rank: 1, Market: "m", Strategy: "s", Signals: []output.SymbolSignal{{Symbol: sym, Direction: dir, Allocation: alloc, Price: price}}}
}

func TestAdvanceCycle(t *testing.T) {
	rules := Rules{StopLoss: 0.1}
	// Cycle 1: open.
	s1, orders, err := Advance(State{}, 1000, []output.Action{action("X", output.Long, 600, 10)}, Prices{{"m", "X"}: 10}, rules, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Cash != 400 || len(s1.Positions) != 1 || orders[0].Kind != output.OrderOpen {
		t.Fatalf("cycle 1: %+v %+v", s1, orders)
	}
	// Re-injection: price +10 % → equity 400 + 660.
	p, err := NextProfile(profile.Profile{Capital: 1, RiskTolerance: 1, Skills: []profile.Skill{}}, s1, Prices{{"m", "X"}: 11})
	if err != nil || p.Capital != 1060 {
		t.Fatalf("NextProfile capital = %d, err %v", p.Capital, err)
	}
	// Cycle 2: same signal → hold.
	s2, orders, _ := Advance(s1, 0, []output.Action{action("X", output.Long, 600, 11)}, Prices{{"m", "X"}: 11}, rules, t0.Add(24*time.Hour))
	if len(s2.Positions) != 1 || orders[0].Kind != output.OrderHold {
		t.Fatalf("cycle 2: %+v", orders)
	}
	// Cycle 3: price drops 15 % from entry → stop-loss, no immediate re-entry.
	s3, orders, _ := Advance(s2, 0, []output.Action{action("X", output.Long, 600, 8.5)}, Prices{{"m", "X"}: 8.5}, rules, t0.Add(48*time.Hour))
	if len(s3.Positions) != 0 || len(orders) != 1 || orders[0].Kind != output.OrderClose || s3.Cash != 400+510 {
		t.Fatalf("cycle 3: %+v %+v", s3, orders)
	}
}

func TestAdvanceDeselected(t *testing.T) {
	prev := State{Cycle: 1, Cash: 0, Positions: []Position{{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 100, EntryPrice: 10}}}
	s, orders, _ := Advance(prev, 0, nil, Prices{{"m", "X"}: 10}, Rules{StopLoss: 0.5}, t0)
	if len(s.Positions) != 0 || orders[0].Kind != output.OrderClose || s.Cash != 100 {
		t.Fatalf("%+v %+v", s, orders)
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if s, err := Load(path); err != nil || s.Cycle != 0 {
		t.Fatalf("missing file should give empty state: %+v %v", s, err)
	}
	want := State{Cycle: 3, UpdatedAt: t0, Cash: 42}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Cycle != 3 || got.Cash != 42 || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestDue(t *testing.T) {
	s := State{Cycle: 1, UpdatedAt: t0}
	if ok, _ := Due(s, time.Hour, t0.Add(30*time.Minute)); ok {
		t.Fatal("should not be due")
	}
	if ok, _ := Due(s, time.Hour, t0.Add(time.Hour)); !ok {
		t.Fatal("should be due")
	}
}

func TestCheckStopsAndCooldown(t *testing.T) {
	rules := Rules{StopLoss: 0.1}
	prev := State{Cycle: 2, Cash: 0, Positions: []Position{
		{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 1000, EntryPrice: 100},
		{Market: "m", Strategy: "s", Symbol: "Y", Direction: 1, Amount: 1000, EntryPrice: 100},
	}}
	s, orders := CheckStops(prev, Prices{{"m", "X"}: 85, {"m", "Y"}: 99}, rules, t0)
	if len(orders) != 1 || orders[0].Symbol != "X" || len(s.Positions) != 1 || s.Cash != 850 {
		t.Fatalf("CheckStops: %+v %+v", s, orders)
	}
	if len(prev.Positions) != 2 {
		t.Fatal("CheckStops must not mutate its input")
	}
	// Next candle: X still long → no re-entry, cooldown kept.
	acts := []output.Action{{Rank: 1, Market: "m", Strategy: "s", Signals: []output.SymbolSignal{
		{Symbol: "X", Direction: output.Long, Allocation: 500, Price: 86},
		{Symbol: "Y", Direction: output.Long, Allocation: 500, Price: 99},
	}}}
	s2, orders, _ := Advance(s, 0, acts, Prices{{"m", "X"}: 86, {"m", "Y"}: 99}, rules, t0)
	for _, o := range orders {
		if o.Kind == output.OrderOpen {
			t.Fatalf("re-entry during cooldown: %+v", o)
		}
	}
	if len(s2.Cooldown) != 1 {
		t.Fatalf("cooldown should persist, got %+v", s2.Cooldown)
	}
	// Signal goes flat → cooldown cleared; then long again → reopened.
	acts[0].Signals[0].Direction = output.Flat
	s3, _, _ := Advance(s2, 0, acts, Prices{{"m", "X"}: 86, {"m", "Y"}: 99}, rules, t0)
	if len(s3.Cooldown) != 0 {
		t.Fatalf("cooldown should be cleared, got %+v", s3.Cooldown)
	}
	acts[0].Signals[0].Direction = output.Long
	_, orders, _ = Advance(s3, 0, acts, Prices{{"m", "X"}: 86, {"m", "Y"}: 99}, rules, t0)
	if len(orders) < 2 || orders[len(orders)-1].Kind != output.OrderOpen || orders[len(orders)-1].Symbol != "X" {
		t.Fatalf("expected X to be reopened: %+v", orders)
	}
}

func TestCloseAll(t *testing.T) {
	prev := State{Cycle: 4, Cash: 100, Positions: []Position{{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 1000, EntryPrice: 10}},
		Cooldown: []Cooldown{{"m", "s", "Y", 1}}}
	s, orders := CloseAll(prev, Prices{{"m", "X"}: 12}, Rules{}, "switch", t0)
	if s.Cash != 1300 || len(s.Positions) != 0 || len(s.Cooldown) != 0 || len(orders) != 1 || orders[0].Reason != "switch" {
		t.Fatalf("%+v %+v", s, orders)
	}
}

func TestAllowOpenGatesEntriesOnly(t *testing.T) {
	asked := 0
	rules := Rules{StopLoss: 0.5, AllowOpen: func(m, s, sym string, dir int) bool {
		asked++
		if m != "m" || s != "s" || sym != "X" || dir != 1 {
			t.Errorf("AllowOpen(%q, %q, %q, %d)", m, s, sym, dir)
		}
		return false
	}}
	st, orders, err := Advance(State{}, 1000, []output.Action{action("X", output.Long, 600, 10)}, Prices{{"m", "X"}: 10}, rules, t0)
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || st.Cash != 1000 || len(st.Positions) != 0 {
		t.Fatalf("a refused entry must leave the cash untouched: %+v", st)
	}
	if len(orders) != 1 || orders[0].Kind != output.OrderProposal || orders[0].Amount != 600 {
		t.Fatalf("orders = %+v, want one proposal", orders)
	}
	// Exits are never gated: a held position whose signal flips is closed.
	held := State{Cycle: 1, Positions: []Position{{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 600, EntryPrice: 10, OpenedAt: t0}}}
	st, orders, _ = Advance(held, 0, []output.Action{action("X", output.Flat, 0, 10)}, Prices{{"m", "X"}: 10}, rules, t0)
	if len(st.Positions) != 0 || orders[0].Kind != output.OrderClose {
		t.Fatalf("exit must not need approval: %+v", orders)
	}
}

func TestMaxHold(t *testing.T) {
	rules := Rules{StopLoss: 0.5, MaxHold: 45 * time.Second}
	held := State{Cycle: 1, Positions: []Position{{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 600, EntryPrice: 10, OpenedAt: t0}}}
	px := Prices{{"m", "X"}: 10.2}
	if st, orders := CheckStops(held, px, rules, t0.Add(44*time.Second)); len(st.Positions) != 1 || len(orders) != 0 {
		t.Fatal("closed before max hold")
	}
	st, orders := CheckStops(held, px, rules, t0.Add(45*time.Second))
	if len(st.Positions) != 0 || len(orders) != 1 || st.Cash != 612 || len(st.Cooldown) != 1 {
		t.Fatalf("max hold not applied: %+v %+v", st, orders)
	}
	// Same through Advance, with the signal still long: no immediate re-entry.
	st, orders, _ = Advance(held, 0, []output.Action{action("X", output.Long, 600, 10.2)}, px, rules, t0.Add(time.Minute))
	if len(st.Positions) != 0 || len(orders) != 1 || orders[0].Kind != output.OrderClose {
		t.Fatalf("Advance: %+v", orders)
	}
}

func TestTradesHistoryAndCosts(t *testing.T) {
	// 15 bps per side on market m.
	rules := Rules{StopLoss: 0.5, CostBps: map[string]float64{"m": 15}}
	held := State{Cycle: 1, Positions: []Position{
		{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 100_000, EntryPrice: 10, OpenedAt: t0},
	}}
	st, orders, _ := Advance(held, 0, []output.Action{action("X", output.Flat, 0, 10.1)}, Prices{{"m", "X"}: 10.1}, rules, t0.Add(20*time.Second))
	if len(st.Trades) != 1 || len(orders) != 1 {
		t.Fatalf("%+v %+v", st, orders)
	}
	tr := st.Trades[0]
	// Value 101 000; fees (100 000 + 101 000) × 0.15 % = 302 (rounded); net 100 698.
	if tr.Fees != 302 || tr.PnL != 698 || st.Cash != 100_698 || tr.ExitPrice != 10.1 || tr.Held() != 20*time.Second {
		t.Fatalf("trade %+v cash %d", tr, st.Cash)
	}
	if tr.Reason != "signal direction changed" || orders[0].PnL != tr.Return() {
		t.Fatalf("trade %+v order %+v", tr, orders[0])
	}
	// A small move does not pay the costs: the trade is a loss.
	st2, _ := CheckStops(State{Cycle: 2, Trades: st.Trades, Positions: []Position{
		{Market: "m", Strategy: "s", Symbol: "X", Direction: 1, Amount: 100_000, EntryPrice: 10, OpenedAt: t0},
	}}, Prices{{"m", "X"}: 10.02}, Rules{StopLoss: 0.5, MaxHold: time.Second, CostBps: rules.CostBps}, t0.Add(time.Minute))
	if len(st2.Trades) != 2 || st2.Trades[1].PnL >= 0 || st2.Trades[0] != tr {
		t.Fatalf("history %+v", st2.Trades)
	}
	// The history is never modified in place: the earlier state is intact.
	if len(st.Trades) != 1 {
		t.Fatal("appending must not alias the previous state's history")
	}
	// CloseAll records too.
	st3, _ := CloseAll(State{Positions: []Position{{Market: "m", Symbol: "Y", Direction: 1, Amount: 500, EntryPrice: 2, OpenedAt: t0}}}, nil, Rules{}, "left devnet", t0)
	if len(st3.Trades) != 1 || st3.Trades[0].PnL != 0 || st3.Trades[0].Reason != "left devnet" {
		t.Fatalf("%+v", st3.Trades)
	}
}

func TestMaxAmountCapsEachEntry(t *testing.T) {
	two := output.Action{Rank: 1, Market: "m", Strategy: "s", Signals: []output.SymbolSignal{
		{Symbol: "X", Direction: output.Long, Allocation: 600, Price: 10},
		{Symbol: "Y", Direction: output.Short, Allocation: 300, Price: 5},
	}}
	prices := Prices{{"m", "X"}: 10, {"m", "Y"}: 5}
	s, orders, err := Advance(State{}, 1000, []output.Action{two}, prices, Rules{StopLoss: 0.1, MaxAmount: 250}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Positions) != 2 || s.Positions[0].Amount != 250 || s.Positions[1].Amount != 250 || s.Cash != 500 {
		t.Fatalf("each entry must be capped at 250: %+v", s)
	}
	// Proposals carry the capped amount too: it is what the user approves.
	gate := Rules{StopLoss: 0.1, MaxAmount: 250, AllowOpen: func(string, string, string, int) bool { return false }}
	_, orders, _ = Advance(State{}, 1000, []output.Action{two}, prices, gate, t0)
	for _, o := range orders {
		if o.Kind != output.OrderProposal || o.Amount != 250 {
			t.Fatalf("proposal %+v", o)
		}
	}
	// No cap: the allocation as before.
	s, _, _ = Advance(State{}, 1000, []output.Action{two}, prices, Rules{StopLoss: 0.1}, t0)
	if s.Positions[0].Amount != 600 {
		t.Fatalf("uncapped: %+v", s.Positions[0])
	}
}
