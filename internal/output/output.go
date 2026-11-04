// Package output implements layer 6: an ordered, traceable list of actions.
// Every number shown comes with the intermediate values that produced it.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/arbiter"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/profile"
)

const (
	Long  = "long"
	Short = "short"
	Flat  = "flat"
)

// DirectionOf maps an exposure to a direction label.
func DirectionOf(exposure float64) string {
	switch {
	case exposure > 0:
		return Long
	case exposure < 0:
		return Short
	default:
		return Flat
	}
}

// Sign maps a direction label to -1, 0 or +1.
func Sign(direction string) int {
	switch direction {
	case Long:
		return 1
	case Short:
		return -1
	default:
		return 0
	}
}

// SymbolSignal is the current opinion of a strategy on one symbol.
type SymbolSignal struct {
	Symbol     string             `json:"symbol"`
	Signal     float64            `json:"signal"`
	Exposure   float64            `json:"exposure"`
	Direction  string             `json:"direction"`
	Allocation int64              `json:"allocation"`
	Price      float64            `json:"price"`
	Trace      map[string]float64 `json:"trace,omitempty"`
}

// Action is one ranked (market, strategy) decision.
type Action struct {
	Rank       int                `json:"rank"`
	Market     string             `json:"market"`
	Strategy   string             `json:"strategy"`
	Allocation int64              `json:"allocation"`
	Weight     float64            `json:"weight"`
	Score      float64            `json:"score"`
	Criterion  string             `json:"criterion"`
	Trace      map[string]float64 `json:"trace"`
	Signals    []SymbolSignal     `json:"signals"`
}

// Order kinds produced by the loop.
const (
	OrderOpen  = "open"
	OrderClose = "close"
	OrderHold  = "hold"
)

// Order is a concrete position change the user should carry out.
type Order struct {
	Kind      string  `json:"kind"`
	Market    string  `json:"market"`
	Strategy  string  `json:"strategy"`
	Symbol    string  `json:"symbol"`
	Direction string  `json:"direction"`
	Amount    int64   `json:"amount"`
	Price     float64 `json:"price"`
	PnL       float64 `json:"pnl,omitempty"`
	Reason    string  `json:"reason"`
}

// Period is the evaluated history window.
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Report is the full output of one cycle.
type Report struct {
	GeneratedAt        time.Time           `json:"generated_at"`
	Cycle              int                 `json:"cycle"`
	Period             Period              `json:"period"`
	Profile            profile.Profile     `json:"profile"`
	Criterion          string              `json:"criterion"`
	CriterionDoc       string              `json:"criterion_doc"`
	Policy             arbiter.Policy      `json:"policy"`
	Capital            int64               `json:"capital"`
	Unallocated        int64               `json:"unallocated"`
	PortfolioCash      int64               `json:"portfolio_cash"`
	Actions            []Action            `json:"actions"`
	Orders             []Order             `json:"orders"`
	Candidates         []arbiter.Candidate `json:"candidates"`
	RejectedCandidates []arbiter.Rejection `json:"rejected_candidates"`
	RejectedMarkets    []market.Rejection  `json:"rejected_markets"`
}

// WriteJSON renders the report as indented JSON (map keys are sorted, so the
// output is deterministic).
func WriteJSON(w io.Writer, r any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Money formats minor units with two decimals.
func Money(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// WriteText renders the report for a terminal.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "route_after_route — cycle %d — %s\n", r.Cycle, r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "period     %s → %s\n", r.Period.From.Format(time.DateOnly), r.Period.To.Format(time.DateOnly))
	fmt.Fprintf(&b, "capital    %s (unallocated %s, portfolio cash %s)\n", Money(r.Capital), Money(r.Unallocated), Money(r.PortfolioCash))
	fmt.Fprintf(&b, "criterion  %s — %s\n", r.Criterion, r.CriterionDoc)
	fmt.Fprintf(&b, "policy     rule=%s max_active=%d cap=%.2f min_score=%.2f max_drawdown=%.0f%%\n",
		r.Policy.Rule, r.Policy.MaxActive, r.Policy.Cap, r.Policy.MinScore, r.Policy.MaxDrawdown*100)

	b.WriteString("\nACTIONS\n")
	if len(r.Actions) == 0 {
		b.WriteString("  (none: no candidate passed arbitration — stay in cash)\n")
	}
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	if len(r.Actions) > 0 {
		fmt.Fprintln(tw, "  #\tMARKET\tSTRATEGY\tSCORE\tWEIGHT\tALLOCATION")
		for _, a := range r.Actions {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%.4f\t%.1f%%\t%s\n", a.Rank, a.Market, a.Strategy, a.Score, a.Weight*100, Money(a.Allocation))
		}
		tw.Flush()
	}
	for _, a := range r.Actions {
		fmt.Fprintf(&b, "\n  [%d] %s / %s — trace:\n", a.Rank, a.Market, a.Strategy)
		writeTrace(&b, "      ", a.Trace)
		for _, s := range a.Signals {
			fmt.Fprintf(&b, "    %-10s signal=%+.3f exposure=%+.2f %-5s alloc=%s price=%g", s.Symbol, s.Signal, s.Exposure, s.Direction, Money(s.Allocation), s.Price)
			if len(s.Trace) > 0 {
				b.WriteString("  {")
				for i, k := range slices.Sorted(maps.Keys(s.Trace)) {
					if i > 0 {
						b.WriteString(" ")
					}
					fmt.Fprintf(&b, "%s=%.4g", k, s.Trace[k])
				}
				b.WriteString("}")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nORDERS\n")
	if len(r.Orders) == 0 {
		b.WriteString("  (none)\n")
	}
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, o := range r.Orders {
		pnl := ""
		if o.Kind != OrderOpen {
			pnl = fmt.Sprintf("pnl=%+.2f%%", o.PnL*100)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s/%s\t%s\t%s\t@%g\t%s\t%s\n", strings.ToUpper(o.Kind), o.Direction, o.Market, o.Symbol, o.Strategy, Money(o.Amount), o.Price, pnl, o.Reason)
	}
	tw.Flush()

	if len(r.RejectedCandidates) > 0 {
		b.WriteString("\nREJECTED CANDIDATES\n")
		for _, x := range r.RejectedCandidates {
			fmt.Fprintf(&b, "  %s / %s (score %.4f): %s\n", x.Market, x.Strategy, x.Score, strings.Join(x.Reasons, "; "))
		}
	}
	if len(r.RejectedMarkets) > 0 {
		b.WriteString("\nREJECTED MARKETS\n")
		for _, x := range r.RejectedMarkets {
			fmt.Fprintf(&b, "  %s: %s\n", x.Market, strings.Join(x.Reasons, "; "))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteCandidates renders the evaluation table (backtest command).
func WriteCandidates(w io.Writer, r Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "period %s → %s — criterion %s\n%s\n\n", r.Period.From.Format(time.DateOnly), r.Period.To.Format(time.DateOnly), r.Criterion, r.CriterionDoc)
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MARKET\tSTRATEGY\tSCORE\tMEAN/BAR\tMAX_DD\tTRADES\tCOSTS\tSYMBOLS(DELISTED)\tSTATUS")
	for _, c := range r.Candidates {
		if len(c.Invalid) > 0 {
			fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\t-\t-\t-\tinvalid: %s\n", c.Market, c.Strategy, strings.Join(c.Invalid, "; "))
			continue
		}
		t := c.Trace
		fmt.Fprintf(tw, "%s\t%s\t%.4f\t%.5f\t%.1f%%\t%.0f\t%.4f\t%.0f(%.0f)\tok\n",
			c.Market, c.Strategy, c.Score, t["mean_return"], t["max_drawdown"]*100, t["trades"], t["costs"], t["symbols"], t["symbols_delisted"])
	}
	tw.Flush()
	_, err := io.WriteString(w, b.String())
	return err
}

func writeTrace(b *strings.Builder, indent string, t map[string]float64) {
	for _, k := range slices.Sorted(maps.Keys(t)) {
		fmt.Fprintf(b, "%s%-24s %.6g\n", indent, k, t[k])
	}
}
