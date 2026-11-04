package app

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/arbiter"
	"github.com/IlyasFounas/route_after_route/internal/eval"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

// Input is everything one cycle needs.
type Input struct {
	Profile    profile.Profile
	Config     Config
	Markets    []market.Market
	Strategies []StrategyInstance
	State      loop.State
	Now        time.Time
}

// Result is the outcome of one cycle.
type Result struct {
	Report     output.Report
	State      loop.State
	Evaluation Evaluation
}

// MarketData is the history of one market, or the error that prevented
// loading it.
type MarketData struct {
	Bars map[string][]market.Bar
	Err  error
}

// Data maps market id → history.
type Data map[string]MarketData

// Prices returns the latest close of every symbol with data.
func (d Data) Prices() loop.Prices {
	prices := loop.Prices{}
	for id, md := range d {
		for sym, bars := range md.Bars {
			if len(bars) > 0 {
				prices[loop.PriceKey{Market: id, Symbol: sym}] = bars[len(bars)-1].Close
			}
		}
	}
	return prices
}

// Fetch loads the history of every selected market over [from, to). Data is
// loaded even for markets the profile may reject: held positions still need
// their prices.
func Fetch(ctx context.Context, markets []market.Market, from, to time.Time) Data {
	data := make(Data, len(markets))
	for _, m := range markets {
		d := m.Descriptor()
		md := MarketData{Bars: map[string][]market.Bar{}}
		for _, sym := range d.Symbols {
			if !sym.OverlapsPeriod(from, to) {
				continue
			}
			bars, err := m.Bars(ctx, sym.Name, from, to)
			if err != nil {
				md.Err = fmt.Errorf("%s: %w", sym.Name, err)
				break
			}
			md.Bars[sym.Name] = bars
		}
		data[d.ID] = md
	}
	return data
}

// Cycle runs layers 1→7 once: Fetch, then Decide.
func Cycle(ctx context.Context, in Input) (Result, error) {
	from, to, err := in.Config.Period.Resolve(in.Now)
	if err != nil {
		return Result{}, err
	}
	return Decide(in, Fetch(ctx, in.Markets, from, to))
}

// Decide runs the full decision on already loaded data: Evaluate, then
// actions, then the portfolio update.
func Decide(in Input, data Data) (Result, error) {
	ev, err := Evaluate(in, data)
	if err != nil {
		return Result{}, err
	}
	actions := ev.Actions(in.Strategies, data, in.Config)
	state, orders, err := loop.Advance(in.State, in.Profile.Capital, actions, data.Prices(), loop.RulesFor(ev.Profile), in.Now)
	if err != nil {
		return Result{}, err
	}
	return Result{Report: ev.Report(in.Now, actions, orders, state), State: state, Evaluation: ev}, nil
}

// Evaluation is the outcome of layers 1 to 5: the effective profile, the
// market selection, every scored candidate and the arbitration.
type Evaluation struct {
	From, To        time.Time
	Profile         profile.Profile
	Accepted        []market.Market
	RejectedMarkets []market.Rejection
	Candidates      []arbiter.Candidate
	Policy          arbiter.Policy
	Decision        arbiter.Decision
}

// Evaluate runs layers 1 to 5 on data. It is the expensive step (walk-forward
// backtests); the live engine runs it periodically, not on every candle.
func Evaluate(in Input, data Data) (Evaluation, error) {
	from, to, err := in.Config.Period.Resolve(in.Now)
	if err != nil {
		return Evaluation{}, err
	}

	// Layer 7 → 1: re-inject portfolio equity as capital.
	p, err := loop.NextProfile(in.Profile, in.State, data.Prices())
	if err != nil {
		return Evaluation{}, err
	}

	// Layer 2: market selection by compatibility.
	accepted, rejectedMarkets := market.Select(in.Markets, p)

	// Layers 3+4: evaluate every (market, strategy) pair in parallel.
	opts := eval.Options{Backtest: in.Config.engineConfig(), WalkForward: in.Config.walkForward(), From: from, To: to}
	cands := make([]arbiter.Candidate, len(accepted)*len(in.Strategies))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, m := range accepted {
		for j, s := range in.Strategies {
			idx := i*len(in.Strategies) + j
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				cands[idx] = evaluate(m.Descriptor(), s, data[m.Descriptor().ID], p.Capital, opts)
			})
		}
	}
	wg.Wait()

	// Layer 5: arbitration.
	policy := in.Config.policy(p)
	dec, err := arbiter.Decide(cands, p.Capital, policy)
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{
		From: from, To: to, Profile: p, Accepted: accepted, RejectedMarkets: rejectedMarkets,
		Candidates: cands, Policy: policy, Decision: dec,
	}, nil
}

// Report assembles the layer 6 output.
func (ev Evaluation) Report(now time.Time, actions []output.Action, orders []output.Order, state loop.State) output.Report {
	return output.Report{
		GeneratedAt:        now,
		Cycle:              state.Cycle,
		Period:             output.Period{From: ev.From, To: ev.To},
		Profile:            ev.Profile,
		Criterion:          eval.Criterion,
		CriterionDoc:       eval.CriterionDoc,
		Policy:             ev.Policy,
		Capital:            ev.Profile.Capital,
		Unallocated:        ev.Decision.Cash,
		PortfolioCash:      state.Cash,
		Actions:            actions,
		Orders:             orders,
		Candidates:         ev.Candidates,
		RejectedCandidates: ev.Decision.Rejected,
		RejectedMarkets:    ev.RejectedMarkets,
	}
}

func evaluate(d market.Descriptor, s StrategyInstance, md MarketData, capital int64, opts eval.Options) arbiter.Candidate {
	c := arbiter.Candidate{Market: d.ID, Strategy: s.Name, MinCapital: d.MinCapital}
	if md.Err != nil {
		c.Invalid = []string{"data: " + md.Err.Error()}
		return c
	}
	if r := strategy.Check(s.Spec(), d, capital); len(r) > 0 {
		c.Invalid = r
		return c
	}
	sc, err := eval.Evaluate(d, md.Bars, s.Strategy, opts)
	if err != nil {
		c.Invalid = []string{"evaluation: " + err.Error()}
		return c
	}
	c.Score, c.Trace = sc.Value, sc.Trace
	return c
}

// Actions computes the current signals of the selected pairs on data (layer
// 6). It is cheap: the live engine calls it on every closed candle with the
// latest Evaluation.
func (ev Evaluation) Actions(strats []StrategyInstance, data Data, cfg Config) []output.Action {
	byMarket := map[string]market.Market{}
	for _, m := range ev.Accepted {
		byMarket[m.Descriptor().ID] = m
	}
	byName := map[string]StrategyInstance{}
	for _, s := range strats {
		byName[s.Name] = s
	}
	var actions []output.Action
	for rank, a := range ev.Decision.Selected {
		d := byMarket[a.Market].Descriptor()
		s := byName[a.Strategy]
		exec := eval.ExecutionConfig(d, cfg.engineConfig())
		// At least 10 s: with 1s candles, a short network hiccup must not
		// silence every signal.
		staleAfter := max(4*d.Granularity.Duration(), 10*time.Second)
		var sigs []output.SymbolSignal
		active := 0
		for _, sym := range d.Symbols {
			bars := data[d.ID].Bars[sym.Name]
			if len(bars) == 0 {
				continue
			}
			last := bars[len(bars)-1]
			if !sym.ActiveAt(last.Time) || ev.To.Sub(last.Time) > staleAfter {
				continue // delisted or no recent data: no tradable signal
			}
			op := s.Signal(bars)
			exp := exec.Exposure(op.Signal)
			if exp != 0 {
				active++
			}
			sigs = append(sigs, output.SymbolSignal{Symbol: sym.Name, Signal: op.Signal, Exposure: exp, Direction: output.DirectionOf(exp), Price: last.Close, Trace: op.Trace})
		}
		// Equal split of the allocation across symbols with a non-flat
		// signal, scaled by the exposure magnitude.
		if active > 0 {
			share := float64(a.Amount) / float64(active)
			for i := range sigs {
				sigs[i].Allocation = int64(math.Floor(share * math.Abs(sigs[i].Exposure)))
			}
		}
		actions = append(actions, output.Action{
			Rank: rank + 1, Market: a.Market, Strategy: a.Strategy, Allocation: a.Amount, Weight: a.Weight,
			Score: a.Score, Criterion: eval.Criterion, Trace: a.Trace, Signals: sigs,
		})
	}
	return actions
}
