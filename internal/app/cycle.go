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
	Report output.Report
	State  loop.State
}

type marketData struct {
	bars map[string][]market.Bar
	err  error
}

// Cycle runs layers 1→7 once.
func Cycle(ctx context.Context, in Input) (Result, error) {
	from, to, err := in.Config.Period.Resolve(in.Now)
	if err != nil {
		return Result{}, err
	}

	// Data for every selected market (needed for held positions' prices even
	// if the market becomes incompatible).
	data := make(map[string]marketData, len(in.Markets))
	prices := loop.Prices{}
	for _, m := range in.Markets {
		d := m.Descriptor()
		md := marketData{bars: map[string][]market.Bar{}}
		for _, sym := range d.Symbols {
			if !sym.OverlapsPeriod(from, to) {
				continue
			}
			bars, err := m.Bars(ctx, sym.Name, from, to)
			if err != nil {
				md.err = fmt.Errorf("%s: %w", sym.Name, err)
				break
			}
			md.bars[sym.Name] = bars
			if len(bars) > 0 {
				prices[loop.PriceKey{Market: d.ID, Symbol: sym.Name}] = bars[len(bars)-1].Close
			}
		}
		data[d.ID] = md
	}

	// Layer 7 → 1: re-inject portfolio equity as capital.
	p, err := loop.NextProfile(in.Profile, in.State, prices)
	if err != nil {
		return Result{}, err
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
		return Result{}, err
	}

	// Layer 6: actions with current signals.
	actions := buildActions(dec, accepted, in.Strategies, data, in.Config, to)

	// Layer 7: apply to portfolio.
	state, orders, err := loop.Advance(in.State, in.Profile.Capital, actions, prices, loop.RulesFor(p), in.Now)
	if err != nil {
		return Result{}, err
	}

	return Result{
		State: state,
		Report: output.Report{
			GeneratedAt:        in.Now,
			Cycle:              state.Cycle,
			Period:             output.Period{From: from, To: to},
			Profile:            p,
			Criterion:          eval.Criterion,
			CriterionDoc:       eval.CriterionDoc,
			Policy:             policy,
			Capital:            p.Capital,
			Unallocated:        dec.Cash,
			PortfolioCash:      state.Cash,
			Actions:            actions,
			Orders:             orders,
			Candidates:         cands,
			RejectedCandidates: dec.Rejected,
			RejectedMarkets:    rejectedMarkets,
		},
	}, nil
}

func evaluate(d market.Descriptor, s StrategyInstance, md marketData, capital int64, opts eval.Options) arbiter.Candidate {
	c := arbiter.Candidate{Market: d.ID, Strategy: s.Name, MinCapital: d.MinCapital}
	if md.err != nil {
		c.Invalid = []string{"data: " + md.err.Error()}
		return c
	}
	if r := strategy.Check(s.Spec(), d, capital); len(r) > 0 {
		c.Invalid = r
		return c
	}
	sc, err := eval.Evaluate(d, md.bars, s.Strategy, opts)
	if err != nil {
		c.Invalid = []string{"evaluation: " + err.Error()}
		return c
	}
	c.Score, c.Trace = sc.Value, sc.Trace
	return c
}

func buildActions(dec arbiter.Decision, markets []market.Market, strats []StrategyInstance, data map[string]marketData, cfg Config, to time.Time) []output.Action {
	byMarket := map[string]market.Market{}
	for _, m := range markets {
		byMarket[m.Descriptor().ID] = m
	}
	byName := map[string]StrategyInstance{}
	for _, s := range strats {
		byName[s.Name] = s
	}
	var actions []output.Action
	for rank, a := range dec.Selected {
		d := byMarket[a.Market].Descriptor()
		s := byName[a.Strategy]
		exec := eval.ExecutionConfig(d, cfg.engineConfig())
		staleAfter := 4 * d.Granularity.Duration()
		var sigs []output.SymbolSignal
		active := 0
		for _, sym := range d.Symbols {
			bars := data[d.ID].bars[sym.Name]
			if len(bars) == 0 {
				continue
			}
			last := bars[len(bars)-1]
			if !sym.ActiveAt(last.Time) || to.Sub(last.Time) > staleAfter {
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
