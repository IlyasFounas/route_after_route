// Package live runs the decision pipeline continuously on real-time market
// data.
//
// Semantics:
//   - every price update (forming candle) refreshes prices, marks the
//     portfolio to market and checks the stop-loss;
//   - every closed candle recomputes the signals of the selected pairs and
//     advances the portfolio (open / hold / close). Signals are never
//     computed on a forming candle: that is not what the backtest evaluated;
//   - every Config.ReevaluateEvery, the full evaluation (walk-forward +
//     arbitration) runs again on the in-memory history;
//   - after a reconnection, missed closed candles are backfilled over REST.
//
// Execution is opt-in (Options.Execute / SetExecution). While it is off, the
// engine keeps evaluating and publishing its advice, but never changes the
// portfolio: no order, no stop-loss.
//
// Entries always need the user's approval: an entry the algorithm wants
// becomes an Opportunity (published in snapshots, with a wall-clock expiry);
// the position is opened only if the user calls Approve before it expires,
// and only if the signal still points the same way. A rejected or expired
// opportunity is not proposed again until its signal has gone flat or
// reversed. Exits (stop-loss, max hold, signal change) are automatic.
//
// The engine clock is the close time of the latest closed candle received,
// not wall time, so a replayed feed gives a deterministic run.
//
// All state is owned by a single goroutine (Run); the outside world talks to
// it through commands and receives immutable Snapshots.
package live

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/app"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
)

// Options configures an Engine.
type Options struct {
	Profile    profile.Profile
	Config     app.Config
	Markets    []market.Market
	Strategies []app.StrategyInstance
	State      loop.State
	// Execute allows the engine to apply its decisions to the portfolio.
	Execute bool
	// StatePath is where the portfolio is saved after every change ("" = never).
	StatePath string
	// SnapshotEvery throttles snapshot publication (default 250 ms).
	SnapshotEvery time.Duration
	// HistoryBars is the number of candles per symbol in snapshots (default 300).
	HistoryBars int
	// TailBars is the number of candles in snapshots for the symbols other
	// than the focused one, once a focus is set (default 300).
	TailBars int
	// Focus is the symbol whose full history (HistoryBars) is published.
	// Zero: every symbol gets HistoryBars.
	Focus loop.PriceKey
	// LogSize is the number of events kept (default 500).
	LogSize int
	// WallClock bounds REST requests and times opportunities (default time.Now).
	WallClock func() time.Time
	// MaxHold closes a position held this long, in engine time (0 = none).
	MaxHold time.Duration
	// ApprovalTTL is how long an opportunity waits for the user (default 10 s).
	ApprovalTTL time.Duration
	// SkipApproval opens entries without asking. Tests only: the
	// applications never set it.
	SkipApproval bool
}

// Event is one line of the engine's journal.
type Event struct {
	Time  time.Time     `json:"time"`
	Kind  string        `json:"kind"` // order, reevaluation, status, error, info
	Text  string        `json:"text"`
	Order *output.Order `json:"order,omitempty"`
}

const (
	EventOrder        = "order"
	EventOpportunity  = "opportunity"
	EventReevaluation = "reevaluation"
	EventStatus       = "status"
	EventError        = "error"
	EventInfo         = "info"
)

// Snapshot is an immutable view of the engine, safe to read from any goroutine.
type Snapshot struct {
	Now              time.Time
	Execute          bool            // whether the bot may execute orders
	BaseProfile      profile.Profile // as configured by the user
	Report           output.Report   // latest decision (actions, candidates, rejections…)
	State            loop.State
	Equity           int64
	Prices           loop.Prices
	Series           map[loop.PriceKey][]market.Bar // closed candles + the forming one last
	Forming          map[loop.PriceKey]bool         // whether the last candle of a series is forming
	Statuses         map[string]market.StreamStatus
	Log              []Event
	NextReevaluation time.Time
	LastError        string
	Markets          []market.Descriptor
	StrategyInfo     map[string]string // strategy name → description
	// Opportunities are the entries waiting for the user's approval, oldest
	// first.
	Opportunities []Opportunity
	// MaxHold is the maximum holding time of a position (0 = none).
	MaxHold time.Duration
}

// Opportunity is an entry the algorithm wants to take, waiting for the
// user's approval. Times are wall-clock.
type Opportunity struct {
	ID        string
	Market    string
	Strategy  string
	Symbol    string
	Direction string  // long or short
	Signal    float64 // strategy signal, in [-1, 1]
	Price     float64 // price when proposed (refreshed on every candle)
	Amount    int64   // capital the entry would commit (minor units)
	At        time.Time
	Expires   time.Time
}

// oppKey identifies an entry: one per (market, strategy, symbol, direction).
type oppKey struct {
	Market, Strategy, Symbol string
	Direction                int
}

func keyOf(o output.Order) oppKey {
	return oppKey{o.Market, o.Strategy, o.Symbol, output.Sign(o.Direction)}
}

type command struct {
	kind    string // reevaluate, execution, profile, reset, approve, reject, focus
	profile profile.Profile
	on      bool
	id      string
	focus   loop.PriceKey
}

type event struct {
	update *market.Update
	status *statusEvent
}

type statusEvent struct {
	market string
	status market.StreamStatus
}

type key = loop.PriceKey

// Engine is the continuous decision loop.
type Engine struct {
	opts Options
	cmds chan command

	subsMu sync.Mutex
	subs   map[chan Snapshot]struct{}

	lastMu sync.Mutex
	last   Snapshot

	// Owned by Run.
	profile    profile.Profile
	data       app.Data
	forming    map[key]market.Bar
	state      loop.State
	ev         app.Evaluation
	evOK       bool
	report     output.Report
	log        []Event
	statuses   map[string]market.StreamStatus
	now        time.Time
	nextReeval time.Time
	execute    bool
	lastErr    string
	dirty      bool
	markets    map[string]market.Market

	costs      map[string]float64
	focus      loop.PriceKey
	opps       map[oppKey]*Opportunity
	suppressed map[oppKey]bool // rejected or expired: wait for the signal to change
	approved   map[oppKey]bool // set only during the advance that follows Approve
	oppSeq     int
}

// New validates the options and builds an engine (not started).
func New(o Options) (*Engine, error) {
	if o.Config.Period.To != "" {
		return nil, errors.New("live: the period must be open-ended (remove \"to\" from the config)")
	}
	if len(o.Markets) == 0 {
		return nil, errors.New("live: no market selected")
	}
	if o.SnapshotEvery <= 0 {
		o.SnapshotEvery = 250 * time.Millisecond
	}
	if o.HistoryBars <= 0 {
		o.HistoryBars = 300
	}
	if o.TailBars <= 0 {
		o.TailBars = 300
	}
	if o.LogSize <= 0 {
		o.LogSize = 500
	}
	if o.ApprovalTTL <= 0 {
		o.ApprovalTTL = 10 * time.Second
	}
	if o.WallClock == nil {
		o.WallClock = time.Now
	}
	e := &Engine{
		opts:     o,
		cmds:     make(chan command, 16),
		subs:     map[chan Snapshot]struct{}{},
		profile:  o.Profile,
		state:    o.State,
		forming:  map[key]market.Bar{},
		statuses: map[string]market.StreamStatus{},
		markets:  map[string]market.Market{},
		execute:  o.Execute,
		focus:    o.Focus,
		costs:    CostBps(o.Markets, o.Config),

		opps:       map[oppKey]*Opportunity{},
		suppressed: map[oppKey]bool{},
		approved:   map[oppKey]bool{},
	}
	for _, m := range o.Markets {
		e.markets[m.Descriptor().ID] = m
	}
	return e, nil
}

// Subscribe returns a channel receiving the latest snapshot (older unread
// snapshots are dropped). Call the returned function to unsubscribe.
func (e *Engine) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, 1)
	e.subsMu.Lock()
	e.subs[ch] = struct{}{}
	e.subsMu.Unlock()
	return ch, func() {
		e.subsMu.Lock()
		delete(e.subs, ch)
		e.subsMu.Unlock()
	}
}

// Last returns the most recently published snapshot.
func (e *Engine) Last() Snapshot {
	e.lastMu.Lock()
	defer e.lastMu.Unlock()
	return e.last
}

// Markets returns the followed markets (fixed at New).
func (e *Engine) Markets() []market.Market { return e.opts.Markets }

// Config returns the engine's configuration (fixed at New).
func (e *Engine) Config() app.Config { return e.opts.Config }

// Reevaluate asks for a full re-evaluation now.
func (e *Engine) Reevaluate() { e.send(command{kind: "reevaluate"}) }

// SetExecution allows (or forbids) the engine to execute its orders. When
// switched on, the current decision is applied immediately.
func (e *Engine) SetExecution(on bool) { e.send(command{kind: "execution", on: on}) }

// SetProfile replaces the user profile and re-evaluates.
func (e *Engine) SetProfile(p profile.Profile) { e.send(command{kind: "profile", profile: p}) }

// ResetPortfolio empties the ledger and restarts from the profile capital.
func (e *Engine) ResetPortfolio() { e.send(command{kind: "reset"}) }

// Approve accepts an opportunity: the entry is taken now if it has not
// expired, execution is allowed and the signal still points the same way.
func (e *Engine) Approve(id string) { e.send(command{kind: "approve", id: id}) }

// Reject dismisses an opportunity until its signal changes.
func (e *Engine) Reject(id string) { e.send(command{kind: "reject", id: id}) }

// SetFocus selects the symbol whose full history is published.
func (e *Engine) SetFocus(k loop.PriceKey) { e.send(command{kind: "focus", focus: k}) }

func (e *Engine) send(c command) {
	select {
	case e.cmds <- c:
	default: // a burst of clicks: drop rather than block the UI
	}
}

// Run loads the history, makes a first decision, then follows the streams
// until ctx is cancelled. It returns nil on cancellation.
func (e *Engine) Run(ctx context.Context) error {
	from, to, err := e.opts.Config.Period.Resolve(e.opts.WallClock())
	if err != nil {
		return err
	}
	e.info("loading history")
	e.data = app.Fetch(ctx, e.opts.Markets, from, to)
	for id, md := range e.data {
		if md.Err != nil {
			e.addEvent(EventError, fmt.Sprintf("%s: %v", id, md.Err), nil)
		}
	}
	e.now = e.latestClose()
	if e.now.IsZero() {
		e.now = e.opts.WallClock().UTC()
	}
	e.reevaluate("startup")
	e.publish()

	events := make(chan event, 1024)
	var wg sync.WaitGroup
	streamCtx, stopStreams := context.WithCancel(ctx)
	defer func() {
		stopStreams()
		wg.Wait()
	}()
	for _, m := range e.opts.Markets {
		syms := e.activeSymbols(m.Descriptor())
		wg.Go(func() { e.follow(streamCtx, m, syms, events) })
	}

	ticker := time.NewTicker(e.opts.SnapshotEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.save()
			e.publish()
			return nil
		case ev := <-events:
			if ev.update != nil {
				e.onUpdate(*ev.update)
			} else {
				e.onStatus(streamCtx, ev.status, events)
			}
		case c := <-e.cmds:
			e.onCommand(c)
		case <-ticker.C:
			e.expireOpportunities()
			if e.dirty {
				e.publish()
			}
		}
	}
}

// follow runs one market's stream (or polling) and forwards its events. A
// final status is always sent after the last update.
func (e *Engine) follow(ctx context.Context, m market.Market, syms []string, events chan<- event) {
	id := m.Descriptor().ID
	sendStatus := func(st market.StreamStatus) {
		select {
		case events <- event{status: &statusEvent{market: id, status: st}}:
		case <-ctx.Done():
		}
	}
	st, ok := m.(market.Streamer)
	if !ok {
		e.poll(ctx, m, syms, events, sendStatus)
		return
	}
	out := make(chan market.Update)
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for u := range out {
			select {
			case events <- event{update: &u}:
			case <-ctx.Done():
			}
		}
	}()
	err := st.Stream(ctx, syms, out, sendStatus)
	close(out)
	<-forwarded
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		sendStatus(market.StreamStatus{State: market.StreamFinished, Err: err})
		return
	}
	sendStatus(market.StreamStatus{State: market.StreamFinished})
}

// poll is the fallback for markets without streaming: fetch new closed
// candles every quarter of a candle (at least every 5 s).
func (e *Engine) poll(ctx context.Context, m market.Market, syms []string, events chan<- event, status func(market.StreamStatus)) {
	d := m.Descriptor()
	every := max(d.Granularity.Duration()/4, 5*time.Second)
	last := map[string]time.Time{}
	status(market.StreamStatus{State: market.StreamConnected})
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		for _, s := range syms {
			from := last[s]
			if from.IsZero() {
				from = e.opts.WallClock().Add(-2 * d.Granularity.Duration())
			}
			bars, err := m.Bars(ctx, s, from, e.opts.WallClock())
			if err != nil {
				status(market.StreamStatus{State: market.StreamReconnecting, Err: err})
				continue
			}
			for _, b := range bars {
				u := market.Update{Market: d.ID, Symbol: s, Bar: b, Closed: true}
				select {
				case events <- event{update: &u}:
				case <-ctx.Done():
					return
				}
				last[s] = b.Time.Add(time.Nanosecond)
			}
		}
	}
}

func (e *Engine) onStatus(ctx context.Context, s *statusEvent, events chan<- event) {
	prev := e.statuses[s.market]
	e.statuses[s.market] = s.status
	e.dirty = true
	text := fmt.Sprintf("%s: %s", s.market, s.status.State)
	if s.status.Err != nil {
		text += " (" + s.status.Err.Error() + ")"
	}
	e.addEvent(EventStatus, text, nil)
	if s.status.State == market.StreamConnected && prev.State == market.StreamReconnecting {
		e.backfill(ctx, s.market, events)
	}
}

// backfill fetches the closed candles missed while disconnected.
func (e *Engine) backfill(ctx context.Context, id string, events chan<- event) {
	m := e.markets[id]
	if m == nil {
		return
	}
	since := map[string]time.Time{}
	for sym, bars := range e.data[id].Bars {
		if len(bars) > 0 {
			since[sym] = bars[len(bars)-1].Time.Add(time.Nanosecond)
		}
	}
	go func() {
		for sym, from := range since {
			bars, err := m.Bars(ctx, sym, from, e.opts.WallClock())
			if err != nil {
				continue
			}
			for _, b := range bars {
				u := market.Update{Market: id, Symbol: sym, Bar: b, Closed: true}
				select {
				case events <- event{update: &u}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func (e *Engine) onUpdate(u market.Update) {
	m := e.markets[u.Market]
	if m == nil {
		return
	}
	k := key{Market: u.Market, Symbol: u.Symbol}
	md := e.data[u.Market]
	if md.Bars == nil {
		md.Bars = map[string][]market.Bar{}
	}
	bars := md.Bars[u.Symbol]
	e.dirty = true

	if !u.Closed {
		if len(bars) > 0 && !u.Bar.Time.After(bars[len(bars)-1].Time) {
			return // stale forming update
		}
		e.forming[k] = u.Bar
		e.checkStops()
		return
	}

	switch {
	case len(bars) == 0 || u.Bar.Time.After(bars[len(bars)-1].Time):
		md.Bars[u.Symbol] = append(bars, u.Bar)
	case u.Bar.Time.Equal(bars[len(bars)-1].Time):
		bars[len(bars)-1] = u.Bar // correction of the last candle
	default:
		return // duplicate or out-of-order candle (e.g. overlapping backfill)
	}
	e.data[u.Market] = md
	if f, ok := e.forming[k]; ok && !f.Time.After(u.Bar.Time) {
		delete(e.forming, k)
	}
	if t := u.Bar.Time.Add(m.Descriptor().Granularity.Duration()); t.After(e.now) {
		e.now = t
	}

	switch {
	case !e.now.Before(e.nextReeval):
		e.reevaluate("scheduled")
	case e.evOK:
		e.advance()
	}
}

// advance recomputes the signals with the current evaluation and, when
// execution is allowed, applies them to the portfolio. Otherwise the orders
// stay hypothetical: they are reported but the portfolio does not change.
//
// Entries go through the approval gate: without the user's approval they come
// out of loop.Advance as proposals, which become Opportunities.
func (e *Engine) advance() {
	ev := e.ev
	ev.To = e.now
	actions := ev.Actions(e.opts.Strategies, e.data, e.opts.Config)
	rules := e.rules(ev.Profile)
	rules.AllowOpen = func(m, st, sym string, dir int) bool {
		return e.execute && (e.opts.SkipApproval || e.approved[oppKey{m, st, sym, dir}])
	}
	state, orders, err := loop.Advance(e.state, e.profile.Capital, actions, e.prices(), rules, e.now)
	if err != nil {
		e.fail(err)
		return
	}
	e.trackOpportunities(actions, orders)
	if !e.execute {
		e.report = ev.Report(e.now, actions, orders, e.state)
		return
	}
	e.state = state
	e.report = ev.Report(e.now, actions, orders, state)
	e.recordOrders(orders)
}

// rules are the exit rules for profile p, with the engine's max hold and
// the markets' trading costs.
func (e *Engine) rules(p profile.Profile) loop.Rules {
	r := loop.RulesFor(p)
	r.MaxHold = e.opts.MaxHold
	r.CostBps = e.costs
	return r
}

// CostBps is the trading cost per side of each market, in basis points: its
// taker fee plus the configured slippage, as in the backtest.
func CostBps(markets []market.Market, cfg app.Config) map[string]float64 {
	out := make(map[string]float64, len(markets))
	for _, m := range markets {
		d := m.Descriptor()
		out[d.ID] = d.Fees.TakerBps + cfg.Backtest.SlippageBps
	}
	return out
}

// trackOpportunities turns the proposals of one advance into opportunities:
// new ones are announced, pending ones refreshed, and those the algorithm no
// longer proposes are dropped. Suppressed entries are released once their
// signal has gone flat or reversed.
func (e *Engine) trackOpportunities(actions []output.Action, orders []output.Order) {
	now := e.opts.WallClock()
	current := map[[3]string]int{}
	for _, a := range actions {
		for _, sg := range a.Signals {
			current[[3]string{a.Market, a.Strategy, sg.Symbol}] = output.Sign(sg.Direction)
		}
	}
	for k := range e.suppressed {
		if current[[3]string{k.Market, k.Strategy, k.Symbol}] != k.Direction {
			delete(e.suppressed, k)
		}
	}
	proposed := map[oppKey]bool{}
	for _, o := range orders {
		k := keyOf(o)
		switch o.Kind {
		case output.OrderOpen:
			delete(e.opps, k)
		case output.OrderProposal:
			if e.suppressed[k] {
				continue
			}
			proposed[k] = true
			if op, ok := e.opps[k]; ok {
				op.Price, op.Amount, op.Signal = o.Price, o.Amount, signalOf(actions, k)
				continue
			}
			e.oppSeq++
			op := &Opportunity{
				ID: fmt.Sprintf("%s-%d", o.Symbol, e.oppSeq), Market: o.Market, Strategy: o.Strategy, Symbol: o.Symbol,
				Direction: o.Direction, Signal: signalOf(actions, k), Price: o.Price, Amount: o.Amount,
				At: now, Expires: now.Add(e.opts.ApprovalTTL),
			}
			e.opps[k] = op
			oc := o
			e.addEvent(EventOpportunity, fmt.Sprintf("opportunity %s: %s %s/%s %s %s @%g (signal %+.3f), waiting for approval",
				op.ID, o.Direction, o.Market, o.Symbol, o.Strategy, output.Money(o.Amount), o.Price, op.Signal), &oc)
		}
	}
	for k, op := range e.opps {
		if !proposed[k] {
			delete(e.opps, k)
			e.info(fmt.Sprintf("opportunity %s withdrawn: the algorithm no longer proposes it", op.ID))
		}
	}
}

func signalOf(actions []output.Action, k oppKey) float64 {
	for _, a := range actions {
		if a.Market != k.Market || a.Strategy != k.Strategy {
			continue
		}
		for _, sg := range a.Signals {
			if sg.Symbol == k.Symbol {
				return sg.Signal
			}
		}
	}
	return 0
}

// expireOpportunities drops the opportunities past their deadline; they are
// suppressed until their signal changes.
func (e *Engine) expireOpportunities() {
	now := e.opts.WallClock()
	for k, op := range e.opps {
		if now.After(op.Expires) {
			delete(e.opps, k)
			e.suppressed[k] = true
			e.info(fmt.Sprintf("opportunity %s expired without approval", op.ID))
		}
	}
}

func (e *Engine) findOpportunity(id string) (oppKey, *Opportunity, bool) {
	for k, op := range e.opps {
		if op.ID == id {
			return k, op, true
		}
	}
	return oppKey{}, nil, false
}

// approve takes the approved entry now, if still possible.
func (e *Engine) approve(id string) {
	e.expireOpportunities()
	k, op, ok := e.findOpportunity(id)
	switch {
	case !ok:
		e.info(fmt.Sprintf("opportunity %s: approval too late (expired or withdrawn)", id))
		return
	case !e.execute:
		e.info(fmt.Sprintf("opportunity %s: not taken, the bot is not allowed to trade", id))
		return
	case !e.evOK:
		return
	}
	e.approved[k] = true
	e.advance()
	delete(e.approved, k)
	if _, still := e.opps[k]; still {
		// Proposed again instead of opened (e.g. no cash left).
		delete(e.opps, k)
		e.suppressed[k] = true
		e.info(fmt.Sprintf("opportunity %s approved but could not be taken", op.ID))
	}
}

func (e *Engine) reject(id string) {
	k, op, ok := e.findOpportunity(id)
	if !ok {
		return
	}
	delete(e.opps, k)
	e.suppressed[k] = true
	e.info(fmt.Sprintf("opportunity %s ignored", op.ID))
}

func (e *Engine) checkStops() {
	if !e.execute || len(e.state.Positions) == 0 {
		return
	}
	rules := e.rules(e.profile)
	if e.evOK {
		rules = e.rules(e.ev.Profile)
	}
	state, orders := loop.CheckStops(e.state, e.prices(), rules, e.now)
	if len(orders) == 0 {
		return
	}
	e.state = state
	e.report.PortfolioCash = state.Cash
	e.recordOrders(orders)
}

func (e *Engine) reevaluate(reason string) {
	from, _, err := e.opts.Config.Period.Resolve(e.now)
	if err == nil {
		e.trim(from)
	}
	in := app.Input{Profile: e.profile, Config: e.opts.Config, Markets: e.opts.Markets, Strategies: e.opts.Strategies, State: e.state, Now: e.now}
	started := time.Now()
	res, err := app.Decide(in, e.data)
	if err != nil {
		e.fail(fmt.Errorf("re-evaluation: %w", err))
		e.nextReeval = e.now.Add(e.opts.Config.ReevaluateEvery.Duration)
		return
	}
	// Only the evaluation is kept: the portfolio moves through advance, so
	// a re-evaluation never opens a position without approval.
	e.ev, e.evOK = res.Evaluation, true
	e.nextReeval = e.now.Add(e.opts.Config.ReevaluateEvery.Duration)
	e.lastErr = ""
	sel := make([]string, len(res.Evaluation.Decision.Selected))
	for i, a := range res.Evaluation.Decision.Selected {
		sel[i] = fmt.Sprintf("%s/%s (%.2f)", a.Market, a.Strategy, a.Score)
	}
	e.addEvent(EventReevaluation, fmt.Sprintf("re-evaluation (%s, %s): %d candidates, selected %v",
		reason, time.Since(started).Round(time.Millisecond), len(res.Evaluation.Candidates), sel), nil)
	e.advance()
	if e.execute {
		e.save()
	}
}

// trim drops candles older than the evaluation window.
func (e *Engine) trim(from time.Time) {
	for id, md := range e.data {
		for sym, bars := range md.Bars {
			i, _ := slices.BinarySearchFunc(bars, from, func(b market.Bar, t time.Time) int { return b.Time.Compare(t) })
			if i > 0 {
				md.Bars[sym] = slices.Clone(bars[i:])
			}
		}
		e.data[id] = md
	}
}

func (e *Engine) onCommand(c command) {
	e.dirty = true
	switch c.kind {
	case "reevaluate":
		e.reevaluate("manual")
	case "execution":
		if c.on == e.execute {
			break
		}
		e.execute = c.on
		if c.on {
			e.info("execution allowed: the bot now executes the algorithm's orders")
			e.reevaluate("execution allowed")
		} else {
			e.info("execution stopped: the algorithm only advises")
		}
	case "profile":
		e.profile = c.profile
		e.info("profile updated")
		e.reevaluate("profile change")
	case "reset":
		e.state = loop.State{Trades: e.state.Trades} // the history is kept
		e.info("ledger reset")
		e.reevaluate("ledger reset")
	case "approve":
		e.approve(c.id)
	case "reject":
		e.reject(c.id)
	case "focus":
		e.focus = c.focus
	}
	e.publish()
}

func (e *Engine) recordOrders(orders []output.Order) {
	changed := false
	for i := range orders {
		o := orders[i]
		if o.Kind == output.OrderHold || o.Kind == output.OrderProposal {
			continue
		}
		changed = true
		e.addEvent(EventOrder, fmt.Sprintf("%s %s %s/%s %s %s @%g — %s", o.Kind, o.Direction, o.Market, o.Symbol, o.Strategy, output.Money(o.Amount), o.Price, o.Reason), &o)
	}
	if changed {
		e.save()
	}
}

func (e *Engine) save() {
	if e.opts.StatePath == "" || e.state.Cycle == 0 {
		return
	}
	if err := loop.Save(e.opts.StatePath, e.state); err != nil {
		e.fail(fmt.Errorf("saving state: %w", err))
	}
}

func (e *Engine) fail(err error) {
	e.lastErr = err.Error()
	e.addEvent(EventError, err.Error(), nil)
}

func (e *Engine) info(text string) { e.addEvent(EventInfo, text, nil) }

func (e *Engine) addEvent(kind, text string, o *output.Order) {
	e.log = append(e.log, Event{Time: e.now, Kind: kind, Text: text, Order: o})
	if over := len(e.log) - e.opts.LogSize; over > 0 {
		e.log = slices.Delete(e.log, 0, over)
	}
	e.dirty = true
}

// prices returns the latest known price of every symbol: the forming candle
// when there is one, otherwise the last close.
func (e *Engine) prices() loop.Prices {
	p := e.data.Prices()
	for k, b := range e.forming {
		p[k] = b.Close
	}
	return p
}

func (e *Engine) latestClose() time.Time {
	var t time.Time
	for _, m := range e.opts.Markets {
		d := m.Descriptor()
		for _, bars := range e.data[d.ID].Bars {
			if len(bars) > 0 {
				if c := bars[len(bars)-1].Time.Add(d.Granularity.Duration()); c.After(t) {
					t = c
				}
			}
		}
	}
	return t
}

func (e *Engine) activeSymbols(d market.Descriptor) []string {
	var out []string
	for _, s := range d.Symbols {
		if s.ActiveAt(e.now) {
			out = append(out, s.Name)
		}
	}
	return out
}

func (e *Engine) publish() {
	prices := e.prices()
	equity, err := e.state.Equity(prices)
	if err != nil || e.state.Cycle == 0 {
		equity = e.profile.Capital
	}
	series := map[key][]market.Bar{}
	forming := map[key]bool{}
	for id, md := range e.data {
		for sym, bars := range md.Bars {
			k := key{Market: id, Symbol: sym}
			limit := e.opts.HistoryBars
			if e.focus != (key{}) && k != e.focus {
				limit = min(limit, e.opts.TailBars)
			}
			n := min(len(bars), limit)
			s := make([]market.Bar, n, n+1)
			copy(s, bars[len(bars)-n:])
			if f, ok := e.forming[k]; ok {
				s = append(s, f)
				forming[k] = true
			}
			series[k] = s
		}
	}
	descs := make([]market.Descriptor, len(e.opts.Markets))
	for i, m := range e.opts.Markets {
		descs[i] = m.Descriptor()
	}
	snap := Snapshot{
		Now:              e.now,
		Execute:          e.execute,
		BaseProfile:      e.profile,
		Report:           e.report,
		State:            cloneState(e.state),
		Equity:           equity,
		Prices:           prices,
		Series:           series,
		Forming:          forming,
		Statuses:         cloneMap(e.statuses),
		Log:              slices.Clone(e.log),
		NextReevaluation: e.nextReeval,
		LastError:        e.lastErr,
		Markets:          descs,
		StrategyInfo:     e.strategyInfo(),
		Opportunities:    e.opportunities(),
		MaxHold:          e.opts.MaxHold,
	}
	e.dirty = false
	e.lastMu.Lock()
	e.last = snap
	e.lastMu.Unlock()
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	for ch := range e.subs {
		select {
		case <-ch: // drop the unread snapshot
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
}

func (e *Engine) opportunities() []Opportunity {
	out := make([]Opportunity, 0, len(e.opps))
	for _, op := range e.opps {
		out = append(out, *op)
	}
	slices.SortFunc(out, func(a, b Opportunity) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func cloneState(s loop.State) loop.State {
	s.Positions = slices.Clone(s.Positions)
	s.Cooldown = slices.Clone(s.Cooldown)
	return s
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *Engine) strategyInfo() map[string]string {
	info := make(map[string]string, len(e.opts.Strategies))
	for _, s := range e.opts.Strategies {
		info[s.Name] = s.Spec().Description
	}
	return info
}
