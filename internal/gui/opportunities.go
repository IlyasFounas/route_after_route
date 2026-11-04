package gui

import (
	"fmt"
	"slices"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
)

// oppCard lists the entries waiting for the user's approval. Rows are rebuilt
// only when the set of opportunities changes, so that a click is never lost
// to a refresh.
type oppCard struct {
	u        *UI
	card     fyne.CanvasObject
	box      *fyne.Container
	ids      []string
	canTrade bool
	rows     map[string]*oppRow
	built    bool
}

type oppRow struct {
	expires   time.Time
	countdown *widget.Label
	detail    *widget.Label
	gain      *canvas.Text // up to +$x at the take-profit
	loss      *canvas.Text // at most −$y at the stop
	odds      *widget.Label
	accept    *widget.Button
	ignore    *widget.Button
}

func newOppCard(u *UI) *oppCard {
	title := text("OPPORTUNITIES", 11, true, themeColor(colorMuted))
	u.mutedTexts = append(u.mutedTexts, title)
	c := &oppCard{u: u, box: container.NewVBox(), rows: map[string]*oppRow{}}
	c.card = newCard(container.NewVBox(title, c.box))
	c.rebuild(nil, false)
	return c
}

func (c *oppCard) apply(s live.Snapshot) {
	ids := make([]string, len(s.Opportunities))
	for i, o := range s.Opportunities {
		ids[i] = o.ID
	}
	if !c.built || !slices.Equal(ids, c.ids) || s.Execute != c.canTrade {
		c.rebuild(s.Opportunities, s.Execute)
		c.u.setTrayOpportunities(s.Opportunities, s.Execute)
	} else {
		for _, o := range s.Opportunities {
			if r := c.rows[o.ID]; r != nil {
				r.detail.SetText(oppDetail(o))
				r.setBracket(o)
			}
		}
	}
	c.tick(timeNow())
}

func (c *oppCard) rebuild(ops []live.Opportunity, canTrade bool) {
	c.built, c.canTrade = true, canTrade
	c.ids = c.ids[:0]
	c.rows = map[string]*oppRow{}
	var objs []fyne.CanvasObject
	if len(ops) == 0 {
		n := len(c.u.settings.Favorites)
		msg := "The algorithm is watching. You are notified as soon as it sees an entry."
		if n > 0 {
			msg = fmt.Sprintf("Watching %d %s. You are notified as soon as the algorithm sees an entry.", n, plural(n, "crypto", "cryptos"))
		}
		empty := widget.NewLabel(msg)
		empty.Wrapping = fyne.TextWrapWord
		empty.Importance = widget.LowImportance
		objs = append(objs, empty)
	}
	for i, o := range ops {
		if i > 0 {
			objs = append(objs, widget.NewSeparator())
		}
		c.ids = append(c.ids, o.ID)
		r := &oppRow{expires: o.Expires}
		head := widget.NewLabel(fmt.Sprintf("%s %s", titleCase(verbOf(o.Direction)), tickerOf(baseOf(o.Symbol))))
		head.TextStyle.Bold = true
		r.countdown = widget.NewLabel("")
		r.countdown.Importance = widget.WarningImportance
		r.detail = widget.NewLabel(oppDetail(o))
		r.detail.Wrapping = fyne.TextWrapWord
		r.detail.SizeName = theme.SizeNameCaptionText
		r.gain = text("", 14, true, themeColor(colorUp))
		r.loss = text("", 14, true, themeColor(colorDown))
		r.odds = widget.NewLabel("")
		r.odds.Wrapping = fyne.TextWrapWord
		r.odds.SizeName = theme.SizeNameCaptionText
		r.odds.Importance = widget.LowImportance
		r.setBracket(o)
		id := o.ID
		r.accept = widget.NewButtonWithIcon("Accept", theme.ConfirmIcon(), func() {
			r.accept.Disable()
			r.ignore.Disable()
			go c.u.be.Approve(id)
		})
		r.accept.Importance = widget.HighImportance
		r.ignore = widget.NewButton("Ignore", func() {
			r.accept.Disable()
			r.ignore.Disable()
			go c.u.be.Reject(id)
		})
		r.ignore.Importance = widget.LowImportance
		objs = append(objs,
			container.NewBorder(nil, nil, container.NewCenter(coinBadge(baseOf(o.Symbol), 28)), r.countdown, head),
			r.detail)
		if o.Hold > 0 {
			objs = append(objs, container.NewGridWithColumns(2, r.gain, r.loss), r.odds)
		}
		if canTrade {
			objs = append(objs, container.NewHBox(r.accept, r.ignore))
		} else {
			hint := widget.NewLabel("Advice only: allow the bot to trade to accept entries.")
			if !c.u.ledgerMode() {
				hint.SetText("Advice only: the public wallet is read-only.")
			}
			hint.Wrapping = fyne.TextWrapWord
			hint.SizeName = theme.SizeNameCaptionText
			hint.Importance = widget.LowImportance
			objs = append(objs, container.NewBorder(nil, nil, nil, r.ignore, hint))
		}
		c.rows[o.ID] = r
	}
	c.box.Objects = objs
	c.box.Refresh()
	if c.u.rightBox != nil {
		c.u.rightBox.Refresh()
	}
}

// tick refreshes the countdowns.
func (c *oppCard) tick(now time.Time) {
	for _, r := range c.rows {
		left := r.expires.Sub(now)
		if left <= 0 {
			r.countdown.SetText("expired")
			r.accept.Disable()
			continue
		}
		r.countdown.SetText(fmt.Sprintf("%d s left", int(left.Round(time.Second).Seconds())))
	}
}

func oppDetail(o live.Opportunity) string {
	return fmt.Sprintf("%s at %s · signal %+.2f · %s", usdCents(o.Amount), priceUSD(o.Price), o.Signal, o.Strategy)
}

// setBracket shows what the entry may gain and lose within the max hold.
func (r *oppRow) setBracket(o live.Opportunity) {
	if o.Hold == 0 {
		return
	}
	g, l, odds := bracketLines(o)
	r.gain.Text, r.loss.Text = g, l
	r.gain.Refresh()
	r.loss.Refresh()
	r.odds.SetText(odds)
}

// bracketLines are the gain line, the loss line and their odds.
func bracketLines(o live.Opportunity) (gain, loss, odds string) {
	gain = "▲ up to " + signedCents(o.MaxGain)
	loss = "▼ at most " + signedCents(-o.MaxLoss)
	odds = fmt.Sprintf("Within %s: take-profit %+.2f%%, reached first in %.0f%% of past %s windows. Stop %.2f%%: the loss is capped there, unless the price jumps past it. Expected %+.2f%% after costs.",
		holdText(o.Hold), o.TakeProfit*100, o.HitRate*100, holdText(o.Hold), -o.StopLoss*100, o.Expected*100)
	return gain, loss, odds
}

// gainLossText is the one-line summary of notifications and the tray.
func gainLossText(o live.Opportunity) string {
	if o.Hold == 0 {
		return ""
	}
	return fmt.Sprintf("up to %s, at most %s within %s", signedCents(o.MaxGain), signedCents(-o.MaxLoss), holdText(o.Hold))
}

func holdText(d time.Duration) string {
	if d < time.Minute || d%time.Minute != 0 {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// watchCard lists the favorites: price, short-term change and what the
// algorithm thinks of each. A click shows it on the chart.
type watchCard struct {
	u     *UI
	card  fyne.CanvasObject
	box   *fyne.Container
	favs  []string
	shown string
	rows  map[string]*watchRow
}

type watchRow struct {
	name   *widget.Label
	state  *widget.Label
	price  *widget.Label
	change *widget.Label
}

func newWatchCard(u *UI) *watchCard {
	title := text("WATCHLIST", 11, true, themeColor(colorMuted))
	u.mutedTexts = append(u.mutedTexts, title)
	add := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), u.showPicker)
	add.Importance = widget.LowImportance
	c := &watchCard{u: u, box: container.NewVBox(), rows: map[string]*watchRow{}}
	c.card = newCard(container.NewVBox(container.NewHBox(container.NewCenter(title), layout.NewSpacer(), add), c.box))
	c.rebuild()
	return c
}

func (c *watchCard) rebuild() {
	c.favs = slices.Clone(c.u.settings.Favorites)
	c.shown = c.u.settings.Pair
	c.rows = map[string]*watchRow{}
	var objs []fyne.CanvasObject
	for _, pair := range c.favs {
		r := &watchRow{name: widget.NewLabel(watchName(pair)), state: widget.NewLabel("…"), price: widget.NewLabel("—"), change: widget.NewLabel("")}
		r.name.TextStyle.Bold = pair == c.shown
		r.state.SizeName = theme.SizeNameCaptionText
		r.state.Importance = widget.LowImportance
		r.price.Alignment = fyne.TextAlignTrailing
		r.change.Alignment = fyne.TextAlignTrailing
		r.change.SizeName = theme.SizeNameCaptionText
		remove := widget.NewButtonWithIcon("", theme.ContentRemoveIcon(), func() { c.remove(pair) })
		remove.Importance = widget.LowImportance
		if len(c.favs) == 1 {
			remove.Disable() // keep at least one
		}
		tap := widget.NewButton("", func() { c.u.showPair(pair) })
		tap.Importance = widget.LowImportance
		if pair == c.shown {
			tap.Importance = widget.MediumImportance
		}
		content := container.NewBorder(nil, nil,
			container.NewCenter(coinBadge(baseOf(pair), 28)),
			container.NewHBox(container.NewVBox(r.price, r.change), container.NewCenter(remove)),
			container.NewVBox(r.name, r.state))
		objs = append(objs, container.NewStack(tap, content))
		c.rows[pair] = r
	}
	c.box.Objects = objs
	c.box.Refresh()
	if c.u.rightBox != nil {
		c.u.rightBox.Refresh()
	}
}

func (c *watchCard) apply(s live.Snapshot) {
	if !slices.Equal(c.favs, c.u.settings.Favorites) || c.shown != c.u.settings.Pair {
		c.rebuild()
	}
	for pair, r := range c.rows {
		k, ok := keyFor(s, pair)
		if !ok {
			r.state.SetText("loading…")
			continue
		}
		r.price.SetText(priceUSD(s.Prices[k]))
		pts, chg := pointsFor(s, pair, "5min")
		if len(pts) > 1 {
			r.change.SetText(arrowPct(chg) + " 5 min")
			r.change.Importance = widget.SuccessImportance
			if chg < 0 {
				r.change.Importance = widget.DangerImportance
			}
			r.change.Refresh()
		}
		r.state.SetText(watchState(s, pair))
	}
}

// watchName is "$SOL · Solana" (the name when known).
func watchName(pair string) string {
	base := baseOf(pair)
	if n := coinName(base); n != "" && n != base {
		return tickerOf(base) + " · " + n
	}
	return tickerOf(base)
}

// watchState says in a few words what the bot does with pair.
func watchState(s live.Snapshot, pair string) string {
	if p, held := botPosition(s, pair); held {
		return "in position · " + shortDuration(max(s.Now.Sub(p.OpenedAt), 0))
	}
	if o, ok := opportunityFor(s, pair); ok {
		return verbOf(o.Direction) + " opportunity"
	}
	if _, sig, ok := signalFor(s, pair); ok && sig.Exposure != 0 {
		return fmt.Sprintf("signal %+.2f", sig.Signal)
	}
	if _, _, ok := signalFor(s, pair); ok {
		return "watching"
	}
	return "no reliable strategy"
}

func (c *watchCard) remove(pair string) {
	u := c.u
	var keep []string
	for _, f := range u.settings.Favorites {
		if f != pair {
			keep = append(keep, f)
		}
	}
	do := func() {
		go func() {
			err := u.be.SetFavorites(keep)
			fyne.Do(func() {
				if err != nil {
					showError(u, err)
					return
				}
				u.settings = u.be.Settings()
				u.refreshCoinHeader()
				u.loadHistory()
				if u.hasSnap {
					u.apply(u.snap)
				}
			})
		}()
	}
	if _, held := botPosition(u.snap, pair); held {
		confirm(u, "Stop watching "+baseOf(pair)+"?",
			"The bot holds a position on "+baseOf(pair)+": it is closed at the current price.", do)
		return
	}
	do()
}

func showError(u *UI, err error) { dialog.ShowError(err, u.win) }

func confirm(u *UI, title, msg string, ok func()) {
	dialog.ShowConfirm(title, msg, func(yes bool) {
		if yes {
			ok()
		}
	}, u.win)
}
