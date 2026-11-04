package live

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/app"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

var t0 = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeMarket serves hist through Bars and streams future (forming + closed).
type fakeMarket struct {
	desc   market.Descriptor
	hist   []market.Bar
	future []market.Bar
}

func (f *fakeMarket) Descriptor() market.Descriptor { return f.desc }

func (f *fakeMarket) Bars(_ context.Context, _ string, from, to time.Time) ([]market.Bar, error) {
	var out []market.Bar
	for _, b := range f.hist {
		if !b.Time.Before(from) && b.Time.Before(to) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeMarket) Stream(ctx context.Context, syms []string, out chan<- market.Update, status func(market.StreamStatus)) error {
	status(market.StreamStatus{State: market.StreamConnected})
	for _, b := range f.future {
		half := b
		half.Close = (b.Open + b.Close) / 2
		half.High, half.Low = math.Max(b.Open, half.Close), math.Min(b.Open, half.Close)
		for _, u := range []market.Update{{Market: f.desc.ID, Symbol: "X", Bar: half}, {Market: f.desc.ID, Symbol: "X", Bar: b, Closed: true}} {
			select {
			case out <- u:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// meanReverting builds a deterministic Ornstein-Uhlenbeck series.
func meanReverting(n int) []market.Bar {
	rng := rand.New(rand.NewPCG(7, 7))
	bars := make([]market.Bar, n)
	p := 100.0
	for i := range bars {
		next := math.Max(1, p+0.2*(100-p)+1.5*rng.NormFloat64())
		bars[i] = market.Bar{Time: t0.AddDate(0, 0, i), Open: p, High: math.Max(p, next) + 0.1, Low: math.Min(p, next) - 0.1, Close: next, Volume: 1}
		p = next
	}
	return bars
}

func newTestEngine(t *testing.T) (*Engine, *fakeMarket) {
	t.Helper()
	bars := meanReverting(400)
	fm := &fakeMarket{
		desc: market.Descriptor{ID: "fake", Kind: market.KindCrypto, Granularity: market.Day,
			Fees: market.Fees{TakerBps: 10}, Access: market.Access{ShortAllowed: true}, Symbols: []market.Symbol{{Name: "X"}}},
		hist: bars[:300], future: bars[300:],
	}
	cfg := app.DefaultConfig()
	cfg.Period = app.PeriodConfig{LookbackDays: 3650}
	cfg.Backtest.TrainBars, cfg.Backtest.TestBars = 100, 30
	cfg.ReevaluateEvery = app.Duration{Duration: 20 * 24 * time.Hour}
	s, err := strategy.New(strategy.ZScoreRevertID, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{
		Profile:       profile.Profile{Capital: 1_000_000, Skills: []profile.Skill{}, RiskTolerance: 10},
		Config:        cfg,
		Markets:       []market.Market{fm},
		Strategies:    []app.StrategyInstance{{Name: s.Spec().ID, Strategy: s}},
		Execute:       true,
		SkipApproval:  true,
		SnapshotEvery: 5 * time.Millisecond,
		WallClock:     func() time.Time { return t0.AddDate(5, 0, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, fm
}

// runToEnd runs the engine until the fake stream is finished.
func runToEnd(t *testing.T) Snapshot { return runToEndWith(t, true) }

func runToEndWith(t *testing.T, execute bool) Snapshot {
	t.Helper()
	e, _ := newTestEngine(t)
	e.execute = execute
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	for e.Last().Statuses["fake"].State != market.StreamFinished {
		if ctx.Err() != nil {
			t.Fatal("timeout waiting for the stream to finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return e.Last()
}

func orderLines(log []Event) []string {
	var out []string
	for _, ev := range log {
		if ev.Kind == EventOrder {
			out = append(out, ev.Time.Format(time.DateOnly)+" "+ev.Text)
		}
	}
	return out
}

func TestEngineReplayIsDeterministic(t *testing.T) {
	a := runToEnd(t)
	wantNow := t0.AddDate(0, 0, 400)
	if !a.Now.Equal(wantNow) {
		t.Fatalf("engine clock = %v, want %v (close of the last candle)", a.Now, wantNow)
	}
	reevals := 0
	for _, ev := range a.Log {
		if ev.Kind == EventReevaluation {
			reevals++
		}
		if ev.Kind == EventError {
			t.Fatalf("unexpected error event: %s", ev.Text)
		}
	}
	// startup + one every 20 candles over 100 streamed candles.
	if reevals != 6 {
		t.Fatalf("re-evaluations = %d, want 6", reevals)
	}
	if a.State.Cycle < 100 {
		t.Fatalf("cycle = %d: the portfolio should advance on every closed candle", a.State.Cycle)
	}
	orders := orderLines(a.Log)
	if len(orders) == 0 {
		t.Fatal("a mean-reverting series should produce orders")
	}
	series := a.Series[loop.PriceKey{Market: "fake", Symbol: "X"}]
	if last := series[len(series)-1]; !last.Time.Equal(t0.AddDate(0, 0, 399)) || a.Forming[loop.PriceKey{Market: "fake", Symbol: "X"}] {
		t.Fatalf("last candle = %v (forming %v)", last.Time, a.Forming)
	}

	b := runToEnd(t)
	sa, _ := json.Marshal(a.State)
	sb, _ := json.Marshal(b.State)
	if string(sa) != string(sb) || strings.Join(orders, "\n") != strings.Join(orderLines(b.Log), "\n") {
		t.Fatal("two runs on the same feed differ")
	}
}

func TestStopLossOnFormingCandle(t *testing.T) {
	e, fm := newTestEngine(t)
	e.data = app.Data{"fake": {Bars: map[string][]market.Bar{"X": fm.hist}}}
	last := fm.hist[len(fm.hist)-1]
	e.now = last.Time.AddDate(0, 0, 1)
	e.state = loop.State{Cycle: 3, Positions: []loop.Position{
		{Market: "fake", Strategy: "zscore_revert", Symbol: "X", Direction: 1, Amount: 100_000, EntryPrice: last.Close},
	}}
	// Risk tolerance 10 → stop-loss at 25 %. A forming candle at -30 % must close.
	crash := last.Close * 0.7
	e.onUpdate(market.Update{Market: "fake", Symbol: "X", Closed: false,
		Bar: market.Bar{Time: e.now, Open: last.Close, High: last.Close, Low: crash, Close: crash}})
	if len(e.state.Positions) != 0 || len(e.state.Cooldown) != 1 {
		t.Fatalf("position should be stopped out: %+v", e.state)
	}
	if n := len(e.log); n == 0 || e.log[n-1].Order == nil || e.log[n-1].Order.Kind != output.OrderClose || !strings.Contains(e.log[n-1].Text, "stop-loss") {
		t.Fatalf("missing stop-loss order event: %+v", e.log)
	}
	// Value 70 000, minus 15 bps on both sides: (100 000 + 70 000) × 0.15 % = 255.
	if e.state.Cash != 69_745 {
		t.Fatalf("cash = %d, want 69745", e.state.Cash)
	}
}

func TestRejectsClosedPeriod(t *testing.T) {
	cfg := app.DefaultConfig()
	cfg.Period = app.PeriodConfig{From: "2020-01-01", To: "2021-01-01"}
	if _, err := New(Options{Config: cfg, Markets: []market.Market{&fakeMarket{}}}); err == nil {
		t.Fatal("a fixed 'to' must be rejected in live mode")
	}
}

func TestNoExecutionMeansAdviceOnly(t *testing.T) {
	s := runToEndWith(t, false)
	if s.Execute {
		t.Fatal("snapshot should report execution off")
	}
	if s.State.Cycle != 0 || len(s.State.Positions) != 0 {
		t.Fatalf("the portfolio must not change without permission: %+v", s.State)
	}
	if got := orderLines(s.Log); len(got) != 0 {
		t.Fatalf("no order may be executed without permission, got %v", got)
	}
	if len(s.Report.Actions) == 0 || len(s.Report.Candidates) == 0 {
		t.Fatal("the advice must keep being computed")
	}
	if s.StrategyInfo["zscore_revert"] == "" {
		t.Fatal("strategy descriptions should be published")
	}
}

func TestStopLossIgnoredWithoutPermission(t *testing.T) {
	e, fm := newTestEngine(t)
	e.execute = false
	e.data = app.Data{"fake": {Bars: map[string][]market.Bar{"X": fm.hist}}}
	last := fm.hist[len(fm.hist)-1]
	e.now = last.Time.AddDate(0, 0, 1)
	e.state = loop.State{Cycle: 3, Positions: []loop.Position{
		{Market: "fake", Strategy: "zscore_revert", Symbol: "X", Direction: 1, Amount: 100_000, EntryPrice: last.Close},
	}}
	crash := last.Close * 0.7
	e.onUpdate(market.Update{Market: "fake", Symbol: "X", Bar: market.Bar{Time: e.now, Open: last.Close, High: last.Close, Low: crash, Close: crash}})
	if len(e.state.Positions) != 1 {
		t.Fatal("without permission the bot must not even execute a stop-loss")
	}
}

// newApprovalEngine is an engine with the approval gate on, the history
// loaded and a first evaluation done. The returned pointer drives its wall
// clock.
func newApprovalEngine(t *testing.T) (*Engine, *fakeMarket, *time.Time) {
	t.Helper()
	e, fm := newTestEngine(t)
	e.opts.SkipApproval = false
	wall := t0.AddDate(5, 0, 0)
	e.opts.WallClock = func() time.Time { return wall }
	e.data = app.Data{"fake": {Bars: map[string][]market.Bar{"X": slices.Clone(fm.hist)}}}
	e.now = e.latestClose()
	e.reevaluate("startup")
	return e, fm, &wall
}

// feedUntilOpportunity streams closed candles until the engine proposes an
// entry.
func feedUntilOpportunity(t *testing.T, e *Engine, fm *fakeMarket) Opportunity {
	t.Helper()
	for _, b := range fm.future {
		if ops := e.opportunities(); len(ops) > 0 {
			return ops[0]
		}
		e.onUpdate(market.Update{Market: "fake", Symbol: "X", Bar: b, Closed: true})
	}
	if ops := e.opportunities(); len(ops) > 0 {
		return ops[0]
	}
	t.Fatal("a mean-reverting series should produce an opportunity")
	return Opportunity{}
}

func TestEntriesNeedApproval(t *testing.T) {
	e, fm, _ := newApprovalEngine(t)
	for _, b := range fm.future {
		e.onUpdate(market.Update{Market: "fake", Symbol: "X", Bar: b, Closed: true})
		e.reevaluate("test") // re-evaluations must not bypass the gate either
	}
	if len(e.state.Positions) != 0 || len(orderLines(e.log)) != 0 {
		t.Fatalf("no entry may be taken without approval: %+v %v", e.state.Positions, orderLines(e.log))
	}
	proposed := 0
	for _, ev := range e.log {
		if ev.Kind == EventOpportunity {
			proposed++
		}
	}
	if proposed == 0 {
		t.Fatal("the algorithm's entries should be proposed as opportunities")
	}
}

func TestApproveOpensPosition(t *testing.T) {
	e, fm, _ := newApprovalEngine(t)
	op := feedUntilOpportunity(t, e, fm)
	e.approve(op.ID)
	if len(e.state.Positions) != 1 {
		t.Fatalf("approved entry not taken: %+v", e.state)
	}
	p := e.state.Positions[0]
	if p.Symbol != op.Symbol || output.DirectionOf(float64(p.Direction)) != op.Direction {
		t.Fatalf("position %+v does not match opportunity %+v", p, op)
	}
	if len(e.opps) != 0 {
		t.Fatalf("a taken opportunity must disappear: %+v", e.opps)
	}
	if lines := orderLines(e.log); len(lines) != 1 || !strings.Contains(lines[0], "open") {
		t.Fatalf("order journal = %v", lines)
	}
}

func TestExpiredOpportunityIsNotTakenNorReproposed(t *testing.T) {
	e, fm, wall := newApprovalEngine(t)
	op := feedUntilOpportunity(t, e, fm)
	*wall = wall.Add(e.opts.ApprovalTTL + time.Second)
	e.expireOpportunities()
	e.approve(op.ID)
	if len(e.state.Positions) != 0 {
		t.Fatal("an expired opportunity must not be taken")
	}
	e.advance() // same candle, same signal
	if len(e.opps) != 0 {
		t.Fatalf("an expired opportunity must not come back while its signal is unchanged: %+v", e.opportunities())
	}
}

func TestRejectSuppressesUntilSignalChanges(t *testing.T) {
	e, fm, _ := newApprovalEngine(t)
	op := feedUntilOpportunity(t, e, fm)
	e.reject(op.ID)
	e.advance()
	if len(e.opps) != 0 {
		t.Fatal("an ignored opportunity must not come back while its signal is unchanged")
	}
	if len(e.suppressed) != 1 {
		t.Fatalf("suppressed = %+v", e.suppressed)
	}
	// The signal goes flat: the entry may be proposed again later.
	e.trackOpportunities([]output.Action{{Market: op.Market, Strategy: op.Strategy,
		Signals: []output.SymbolSignal{{Symbol: op.Symbol, Direction: output.Flat}}}}, nil)
	if len(e.suppressed) != 0 {
		t.Fatal("suppression must end once the signal changes")
	}
}

func TestApproveWithoutExecutionDoesNothing(t *testing.T) {
	e, fm, _ := newApprovalEngine(t)
	e.execute = false
	op := feedUntilOpportunity(t, e, fm)
	before := e.state
	e.approve(op.ID)
	if len(e.state.Positions) != 0 || e.state.Cycle != before.Cycle || e.state.Cash != before.Cash {
		t.Fatalf("without permission to trade, an approval must not change the ledger: %+v", e.state)
	}
}

func TestMaxHoldClosesAutomatically(t *testing.T) {
	e, fm, _ := newApprovalEngine(t)
	e.opts.MaxHold = 24 * time.Hour // one candle of the fake daily market
	op := feedUntilOpportunity(t, e, fm)
	e.approve(op.ID)
	if len(e.state.Positions) != 1 {
		t.Fatal("approved entry not taken")
	}
	opened := e.state.Positions[0].OpenedAt
	for _, b := range fm.future {
		if !b.Time.After(opened) {
			continue
		}
		e.onUpdate(market.Update{Market: "fake", Symbol: "X", Bar: b, Closed: true})
		break
	}
	if len(e.state.Positions) != 0 {
		t.Fatalf("the position should be closed after max hold: %+v", e.state.Positions)
	}
	closed := false
	for _, ev := range e.log {
		if ev.Order != nil && ev.Order.Kind == output.OrderClose && strings.Contains(ev.Order.Reason, "max hold") {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("missing max-hold close: %v", orderLines(e.log))
	}
	// The closed position is in the history, net of the market's costs
	// (10 bps taker + 5 bps slippage per side).
	if len(e.state.Trades) != 1 {
		t.Fatalf("history = %+v", e.state.Trades)
	}
	tr := e.state.Trades[0]
	if tr.Symbol != "X" || tr.Fees <= 0 || tr.ExitPrice <= 0 || !strings.Contains(tr.Reason, "max hold") {
		t.Fatalf("trade %+v", tr)
	}
	if e.costs["fake"] != 15 {
		t.Fatalf("costs %v", e.costs)
	}
	// A reset keeps the history.
	e.onCommand(command{kind: "reset"})
	if len(e.state.Trades) != 1 {
		t.Fatal("the history must survive a ledger reset")
	}
}
