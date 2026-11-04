package gui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/output"
)

// whyMarkdown explains the current advice in plain words, with the numbers
// that produced it: nothing about a decision is hidden.
func whyMarkdown(s live.Snapshot, pair string) string {
	base := baseOf(pair)
	adv := adviceFor(s, pair)
	p := s.Report.Profile
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n\n%s\n\n", adv.Word, base, adv.Line)

	if a, sig, ok := signalFor(s, pair); ok {
		t := a.Trace
		fmt.Fprintf(&b, "### The strategy in charge: %s\n\n", a.Strategy)
		if d := s.StrategyInfo[a.Strategy]; d != "" {
			fmt.Fprintf(&b, "%s\n\n", d)
		}
		b.WriteString("**How it would have done** (replayed on the past, on periods it had not seen, fees included):\n\n")
		fmt.Fprintf(&b, "- Risk-adjusted return (Sharpe): **%.2f**\n", a.Score)
		fmt.Fprintf(&b, "- Worst drop from a peak: **%.1f%%**\n", t["max_drawdown"]*100)
		fmt.Fprintf(&b, "- Trades: %.0f, fees and slippage paid: %.1f%% in total\n\n", t["trades"], t["costs"]*100)
		b.WriteString("**What it reads now:**\n\n")
		for _, k := range slices.Sorted(maps.Keys(sig.Trace)) {
			fmt.Fprintf(&b, "- %s: %.4g\n", k, sig.Trace[k])
		}
		fmt.Fprintf(&b, "- signal: %+.3f → share of the budget: %.0f%%\n\n", sig.Signal, sig.Exposure*100)
		fmt.Fprintf(&b, "Budget given to this strategy: %s (%.0f%% of the balance).\n\n", usdCents(a.Allocation), a.Weight*100)
	}

	if len(s.Report.RejectedCandidates) > 0 {
		b.WriteString("### Strategies set aside\n\n")
		for _, r := range s.Report.RejectedCandidates {
			fmt.Fprintf(&b, "- **%s** (score %.2f): %s\n", r.Strategy, r.Score, strings.Join(r.Reasons, "; "))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### Your limits\n\n- Risk tolerance %d/10: a strategy is rejected if it ever lost more than **%.0f%%** from a peak.\n"+
		"- Stop-loss: a position is closed if it loses **%.1f%%**.\n\n", p.RiskTolerance, p.MaxDrawdown()*100, p.MaxDrawdown()*50)

	var acts []string
	for i := len(s.Log) - 1; i >= 0 && len(acts) < 10; i-- {
		ev := s.Log[i]
		if o := ev.Order; o != nil && ev.Kind == live.EventOrder {
			verb := "Bought"
			if o.Kind == output.OrderClose {
				verb = "Sold"
			}
			acts = append(acts, fmt.Sprintf("- %s · %s %s of %s at %s — %s", ev.Time.Local().Format("Jan 2, 15:04"),
				verb, usdCents(o.Amount), baseOf(o.Symbol), priceUSD(o.Price), o.Reason))
		}
	}
	b.WriteString("### Recent bot activity\n\n")
	if len(acts) == 0 {
		b.WriteString("No order yet.\n")
	} else {
		b.WriteString(strings.Join(acts, "\n") + "\n")
	}
	return b.String()
}

func (u *UI) showWhy() {
	rt := widget.NewRichTextFromMarkdown(whyMarkdown(u.snap, u.settings.Pair))
	rt.Wrapping = fyne.TextWrapWord
	d := dialog.NewCustom("Why this advice?", "Close", container.NewVScroll(rt), u.win)
	d.Resize(fyne.NewSize(620, 620))
	d.Show()
}
