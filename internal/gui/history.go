package gui

import (
	"fmt"
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/loop"
)

// historyRows is the number of recent trades shown in the card.
const historyRows = 3

// TradeSummary sums up the closed trades.
type TradeSummary struct {
	Trades, Won, Lost int
	PnL, Fees         int64 // net gain (negative: loss) and costs, minor units
	Best, Worst       loop.Trade
}

// WinRate is the share of winning trades.
func (s TradeSummary) WinRate() float64 {
	if s.Trades == 0 {
		return 0
	}
	return float64(s.Won) / float64(s.Trades)
}

// summarize computes the summary of trades.
func summarize(trades []loop.Trade) TradeSummary {
	var s TradeSummary
	for i, t := range trades {
		s.Trades++
		s.PnL += t.PnL
		s.Fees += t.Fees
		switch {
		case t.PnL > 0:
			s.Won++
		case t.PnL < 0:
			s.Lost++
		}
		if i == 0 || t.PnL > s.Best.PnL {
			s.Best = t
		}
		if i == 0 || t.PnL < s.Worst.PnL {
			s.Worst = t
		}
	}
	return s
}

// summaryLine is the summary in one sentence.
func summaryLine(s TradeSummary) string {
	if s.Trades == 0 {
		return "No closed trade yet."
	}
	return fmt.Sprintf("%d %s · %d won, %d lost (%.0f%% won) · net %s · fees %s",
		s.Trades, plural(s.Trades, "trade", "trades"), s.Won, s.Lost, s.WinRate()*100, signedCents(s.PnL), usdCents(s.Fees))
}

// signedCents formats a gain or a loss, e.g. +$1.23 or −$0.40.
func signedCents(c int64) string {
	switch {
	case c > 0:
		return "+" + usdCents(c)
	case c < 0:
		return "−" + usdCents(-c)
	default:
		return usdCents(0)
	}
}

func pnlImportance(c int64) widget.Importance {
	switch {
	case c > 0:
		return widget.SuccessImportance
	case c < 0:
		return widget.DangerImportance
	default:
		return widget.MediumImportance
	}
}

func directionWord(d int) string {
	if d < 0 {
		return "short"
	}
	return "long"
}

// tradeTitle is "SOL long · +$1.23 (+0.45%)".
func tradeTitle(t loop.Trade) string {
	return fmt.Sprintf("%s %s · %s (%s)", tickerOf(baseOf(t.Symbol)), directionWord(t.Direction), signedCents(t.PnL), pct(t.Return()))
}

// tradeDetail is when, how long, at which prices, how much and why.
func tradeDetail(t loop.Trade) string {
	return fmt.Sprintf("%s to %s (%s) · in at %s, out at %s · %s · fees %s · %s",
		t.OpenedAt.Local().Format("Jan 2 15:04:05"), t.ClosedAt.Local().Format("15:04:05"), shortDuration(max(t.Held(), 0)),
		priceUSD(t.EntryPrice), priceUSD(t.ExitPrice), usdCents(t.Amount), usdCents(t.Fees), t.Reason)
}

// historyCard shows the summary and the last trades; "See all" opens the
// full history. It is rebuilt only when a trade is added.
type historyCard struct {
	u       *UI
	card    fyne.CanvasObject
	box     *fyne.Container
	trades  []loop.Trade
	summary *widget.Label
	built   bool
}

func newHistoryCard(u *UI) *historyCard {
	title := text("HISTORY", 11, true, themeColor(colorMuted))
	u.mutedTexts = append(u.mutedTexts, title)
	all := widget.NewButtonWithIcon("See all", theme.ListIcon(), func() { u.show(screenHistory) })
	all.Importance = widget.LowImportance
	c := &historyCard{u: u, box: container.NewVBox()}
	c.summary = widget.NewLabel("")
	c.summary.Wrapping = fyne.TextWrapWord
	c.card = newCard(container.NewVBox(
		container.NewHBox(container.NewCenter(title), layout.NewSpacer(), all),
		c.summary, c.box))
	c.rebuild(nil)
	return c
}

func (c *historyCard) apply(s live.Snapshot) {
	if c.built && len(s.State.Trades) == len(c.trades) {
		return
	}
	c.rebuild(s.State.Trades)
}

func (c *historyCard) rebuild(trades []loop.Trade) {
	c.built, c.trades = true, trades
	sum := summarize(trades)
	c.summary.SetText(summaryLine(sum))
	c.summary.Importance = pnlImportance(sum.PnL)
	if sum.Trades == 0 {
		c.summary.Importance = widget.LowImportance
	}
	c.summary.Refresh()
	var objs []fyne.CanvasObject
	for i := len(trades) - 1; i >= 0 && len(trades)-i <= historyRows; i-- {
		objs = append(objs, tradeRow(trades[i], false))
	}
	c.box.Objects = objs
	c.box.Refresh()
	if c.u.rightBox != nil {
		c.u.rightBox.Refresh()
	}
}

// tradeRow is one trade: the result, then the details (full: every detail).
func tradeRow(t loop.Trade, full bool) fyne.CanvasObject {
	head := widget.NewLabel(tradeTitle(t))
	head.TextStyle.Bold = true
	head.Importance = pnlImportance(t.PnL)
	sub := widget.NewLabel(fmt.Sprintf("%s · held %s · %s", t.ClosedAt.Local().Format("15:04:05"), shortDuration(max(t.Held(), 0)), t.Reason))
	if full {
		sub.SetText(tradeDetail(t))
	}
	sub.Wrapping = fyne.TextWrapWord
	sub.SizeName = theme.SizeNameCaptionText
	sub.Importance = widget.LowImportance
	return container.NewBorder(nil, nil, container.NewCenter(coinBadge(baseOf(t.Symbol), 26)), nil,
		container.NewVBox(head, sub))
}

// historyView is the "historic" screen: the summary, then every closed
// trade, newest first. It follows the snapshots while shown.
type historyView struct {
	u        *UI
	content  fyne.CanvasObject
	summary  *widget.Label
	extremes *widget.Label
	empty    *widget.Label
	list     *widget.List
	trades   []loop.Trade // newest first
	n        int
	ledger   bool
	built    bool
}

func newHistoryView(u *UI) *historyView {
	v := &historyView{u: u}
	title := text("HISTORY", 11, true, themeColor(colorMuted))
	u.mutedTexts = append(u.mutedTexts, title)
	v.summary = widget.NewLabel("")
	v.summary.Wrapping = fyne.TextWrapWord
	v.summary.TextStyle.Bold = true
	v.extremes = widget.NewLabel("")
	v.extremes.Wrapping = fyne.TextWrapWord
	v.extremes.SizeName = theme.SizeNameCaptionText
	v.extremes.Importance = widget.LowImportance
	v.empty = widget.NewLabel("")
	v.empty.Wrapping = fyne.TextWrapWord
	v.empty.Importance = widget.LowImportance
	v.list = widget.NewList(
		func() int { return len(v.trades) },
		func() fyne.CanvasObject { return container.NewStack() },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			st := o.(*fyne.Container)
			st.Objects = []fyne.CanvasObject{tradeRow(v.trades[i], true)}
			st.Refresh()
		},
	)
	header := container.NewVBox(
		container.NewHBox(u.homeButton(), container.NewCenter(title)),
		v.summary, v.extremes, widget.NewSeparator())
	v.content = container.New(layout.NewCustomPaddedLayout(14, 14, 14, 14),
		newCard(container.NewBorder(header, nil, nil, nil, container.NewStack(v.list, v.empty))))
	v.apply(u.snap)
	return v
}

func (v *historyView) apply(s live.Snapshot) {
	if v.built && len(s.State.Trades) == v.n && v.u.ledgerMode() == v.ledger {
		return
	}
	v.built, v.n, v.ledger = true, len(s.State.Trades), v.u.ledgerMode()
	v.trades = slices.Clone(s.State.Trades)
	slices.Reverse(v.trades)
	sum := summarize(v.trades)
	v.summary.SetText(summaryLine(sum))
	v.summary.Importance = pnlImportance(sum.PnL)
	v.summary.Refresh()
	v.extremes.SetText("")
	if sum.Trades > 0 {
		v.extremes.SetText(fmt.Sprintf("Best: %s · worst: %s. Gains and losses are net of the trading costs (fee + slippage on both sides).",
			tradeTitle(sum.Best), tradeTitle(sum.Worst)))
	}
	switch {
	case len(v.trades) > 0:
		v.empty.Hide()
	case !v.u.ledgerMode():
		v.empty.SetText("The public wallet has no ledger. Switch the wallet to devnet in trading to let the bot trade: its closed trades will appear here.")
		v.empty.Show()
	default:
		v.empty.SetText("The bot has not closed any trade yet. Every closed trade appears here, with what it gained or lost.")
		v.empty.Show()
	}
	for i := range v.trades {
		v.list.SetItemHeight(i, 74)
	}
	v.list.Refresh()
}
