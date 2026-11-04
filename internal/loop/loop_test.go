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
