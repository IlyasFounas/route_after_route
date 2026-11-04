// Package gui is the desktop interface (Fyne). It shows the watched cryptos,
// the algorithm's advice and opportunities, the bot permission, the devnet
// ledger and the wallet. It never computes a decision: it renders engine
// snapshots and forwards the user's choices to the Backend.
package gui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/output"
)

// UI is the main window.
type UI struct {
	app fyne.App
	win fyne.Window
	be  Backend

	settings  Settings
	demo      bool
	snap      live.Snapshot
	hasSnap   bool
	wal       WalletView
	hidden    bool
	lastEvent live.Event
	switching string // pair being loaded
	trayMenu  *fyne.Menu
	trayShow  *fyne.MenuItem
	trayQuit  *fyne.MenuItem

	// Long-range curve (1D, 1W, 1M), fetched for display.
	longKey string // pair/range of longPts
	longPts []Point
	longErr string
	longAt  time.Time

	// Price card.
	badgeBox   *fyne.Container
	coinName   *canvas.Text
	coinTicker *canvas.Text
	price      *canvas.Text
	changeBg   *canvas.Rectangle
	changeText *canvas.Text
	changeSpan *canvas.Text
	rangeBtns  map[string]*widget.Button
	chart      *LineChart
	statusDot  *canvas.Circle
	statusText *canvas.Text

	// Static texts recoloured on theme changes.
	fgTexts, mutedTexts []*canvas.Text

	// Bot card.
	adviceDot  *canvas.Circle
	adviceWord *canvas.Text
	adviceLine *widget.Label
	toggle     *Toggle
	toggleRow  *fyne.Container
	toggleHint *widget.Label
	ledgerBox  *fyne.Container
	ledgerName *canvas.Text
	balance    *canvas.Text
	balancePnl *canvas.Text
	position   *widget.Label

	// Opportunities and watchlist cards.
	opps  *oppCard
	watch *watchCard
	hist  *historyCard

	// Screens: home prompt, trading, history.
	screen     screen
	screens    []fyne.CanvasObject
	prompt     *widget.Entry
	promptHint *canvas.Text
	histView   *historyView

	// Effort indicator, in the top bar.
	effort *effortChip

	// Wallet card.
	walletBox *fyne.Container
	rightBox  *fyne.Container
	leftBox   *fyne.Container // re-laid out when texts change width
	botBox    *fyne.Container
}

// New builds the window.
func New(a fyne.App, be Backend) *UI {
	a.Settings().SetTheme(WarmTheme{})
	u := &UI{app: a, be: be, settings: be.Settings(), demo: be.Demo()}
	logos = newLogoStore(be.Logo)
	if !ValidRange(u.settings.Range) {
		u.settings.Range = DefaultRange
	}
	u.win = a.NewWindow("route_after_route")
	u.win.Resize(fyne.NewSize(1180, 760))
	trading := u.build()
	home := u.buildHome()
	u.histView = newHistoryView(u)
	u.screens = []fyne.CanvasObject{screenHome: home, screenTrading: trading, screenHistory: u.histView.content}
	u.win.SetContent(container.NewStack(u.screens...))
	u.win.Canvas().SetOnTypedKey(u.onKey)
	u.setupTray()
	u.win.SetCloseIntercept(u.onClose)
	u.refreshCoinHeader()
	devnet := u.settings.WalletMode == ModeDevnet && !u.demo
	u.applyWallet(WalletView{Devnet: devnet, Address: u.settings.Wallet, Loading: devnet || u.settings.Wallet != ""})
	u.refreshMode()
	u.show(screenHome)
	a.Settings().AddListener(func(fyne.Settings) {
		fyne.Do(func() {
			u.recolor()
			u.applyWallet(u.wal)
			if u.hasSnap {
				u.apply(u.snap)
			}
		})
	})
	return u
}

// recolor applies the current theme to the texts built once.
func (u *UI) recolor() {
	for _, t := range u.fgTexts {
		t.Color = themeColor(theme.ColorNameForeground)
		t.Refresh()
	}
	for _, t := range u.mutedTexts {
		t.Color = themeColor(colorMuted)
		t.Refresh()
	}
	u.chart.dataGen++
	u.chart.Refresh()
}

// Window returns the main window.
func (u *UI) Window() fyne.Window { return u.win }

// Start consumes snapshots and wallet updates until the app exits.
func (u *UI) Start() {
	go func() {
		for s := range u.be.Snapshots() {
			fyne.Do(func() { u.apply(s) })
		}
	}()
	go func() {
		for w := range u.be.Wallets() {
			fyne.Do(func() { u.applyWallet(w) })
		}
	}()
	// Opportunity countdowns, and the long-range curve refresh.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			fyne.Do(u.tick)
		}
	}()
	u.loadHistory()
}

func (u *UI) tick() {
	u.opps.tick(timeNow())
	if !liveRange(u.settings.Range) && timeNow().Sub(u.longAt) > time.Minute {
		u.loadHistory()
	}
}

func (u *UI) build() fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)

	// Coin selector: a flat button under the badge, name and chevron.
	u.badgeBox = container.NewStack()
	u.coinName = text("", 20, true, fg)
	u.coinTicker = text("", 13, false, muted)
	chevron := widget.NewIcon(theme.MenuDropDownIcon())
	selector := container.NewStack(
		widget.NewButton("", u.showPicker),
		container.New(layout.NewCustomPaddedLayout(6, 6, 8, 12),
			container.NewHBox(u.badgeBox, container.NewVBox(u.coinName, u.coinTicker), chevron)),
	)

	u.price = text("—", 40, true, fg)
	u.changeBg = canvas.NewRectangle(color.Transparent)
	u.changeBg.CornerRadius = 12
	u.changeText = text("", 13, true, fg)
	u.changeSpan = text("", 13, false, muted)
	pill := container.NewStack(u.changeBg, container.New(layout.NewCustomPaddedLayout(3, 3, 10, 10), u.changeText))
	priceBlock := container.NewVBox(u.price, container.NewHBox(pill, u.changeSpan))

	u.rangeBtns = map[string]*widget.Button{}
	ranges := container.NewHBox()
	for _, r := range Ranges {
		b := widget.NewButton(rangeInfo[r].label, func() { u.setRange(r) })
		u.rangeBtns[r] = b
		ranges.Add(b)
	}
	u.styleRanges()
	u.statusDot = canvas.NewCircle(muted)
	dotBox := container.NewCenter(container.NewGridWrap(fyne.NewSize(9, 9), u.statusDot))
	u.statusText = text("Connecting…", 12, false, muted)
	footer := container.NewHBox(ranges, layout.NewSpacer(), dotBox, u.statusText)

	u.chart = newLineChart()
	u.fgTexts = append(u.fgTexts, u.coinName)
	u.mutedTexts = append(u.mutedTexts, u.coinTicker, u.changeSpan)
	header := container.NewVBox(container.NewHBox(selector), container.New(layout.NewCustomPaddedLayout(8, 6, 6, 0), priceBlock))
	u.leftBox = container.NewBorder(header, footer, nil, nil, u.chart)
	left := newCard(u.leftBox)

	u.opps = newOppCard(u)
	u.watch = newWatchCard(u)
	u.hist = newHistoryCard(u)
	u.rightBox = container.NewVBox(u.buildTopBar(), u.opps.card, u.buildBotCard(), u.watch.card, u.hist.card, u.buildWalletCard())
	body := container.NewBorder(nil, nil, nil,
		fixedWidth(container.NewVScroll(u.rightBox), 360),
		left)
	return container.New(layout.NewCustomPaddedLayout(14, 14, 14, 14), body)
}

func (u *UI) buildTopBar() fyne.CanvasObject {
	name := text("route_after_route", 13, true, themeColor(colorMuted))
	u.mutedTexts = append(u.mutedTexts, name)
	gear := widget.NewButtonWithIcon("", theme.SettingsIcon(), u.showSettings)
	gear.Importance = widget.LowImportance
	u.effort = newEffortChip(u.currentEffort(), u.showEffort)
	return container.NewHBox(u.homeButton(), container.NewCenter(name), layout.NewSpacer(), container.NewCenter(u.effort), gear)
}

func (u *UI) buildBotCard() fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)
	title := text("ALGORITHM", 11, true, muted)

	u.adviceDot = canvas.NewCircle(muted)
	u.adviceWord = text("…", 28, true, fg)
	dot := container.NewCenter(container.NewGridWrap(fyne.NewSize(12, 12), u.adviceDot))
	u.adviceLine = widget.NewLabel("The algorithm is studying the market.")
	u.adviceLine.Wrapping = fyne.TextWrapWord
	why := widget.NewButtonWithIcon("Why?", theme.QuestionIcon(), u.showWhy)
	why.Importance = widget.LowImportance

	u.toggle = newToggle(u.settings.Execute, u.onToggle)
	allow := widget.NewLabel("Allow trading on devnet")
	if u.demo {
		allow.SetText("Allow trading (demo)")
	}
	allow.TextStyle.Bold = true
	allow.Wrapping = fyne.TextWrapWord
	u.toggleHint = widget.NewLabel("")
	u.toggleHint.Wrapping = fyne.TextWrapWord
	u.toggleHint.SizeName = theme.SizeNameCaptionText

	u.balance = text("—", 22, true, fg)
	u.balancePnl = text("", 13, true, muted)
	u.ledgerName = text("DEVNET LEDGER", 11, true, muted)
	if u.demo {
		u.ledgerName.Text = "DEMO LEDGER"
	}
	u.fgTexts = append(u.fgTexts, u.balance)
	u.mutedTexts = append(u.mutedTexts, title, u.ledgerName)
	u.position = widget.NewLabel("")
	u.position.Wrapping = fyne.TextWrapWord
	u.position.SizeName = theme.SizeNameCaptionText

	u.toggleRow = container.NewBorder(nil, nil, nil, container.NewCenter(u.toggle), allow)
	u.ledgerBox = container.NewVBox(
		widget.NewSeparator(),
		u.ledgerName,
		container.NewHBox(u.balance, container.NewCenter(u.balancePnl)),
		u.position,
	)
	u.botBox = container.NewVBox(
		title,
		container.NewHBox(dot, u.adviceWord),
		u.adviceLine,
		container.NewHBox(why),
		widget.NewSeparator(),
		u.toggleRow,
		u.toggleHint,
		u.ledgerBox,
	)
	return newCard(u.botBox)
}

func (u *UI) buildWalletCard() fyne.CanvasObject {
	u.walletBox = container.NewVBox()
	return newCard(u.walletBox)
}

func (u *UI) setupTray() {
	desk, ok := u.app.(desktop.App)
	if !ok {
		return
	}
	u.trayQuit = fyne.NewMenuItem("Quit", u.doQuit)
	u.trayQuit.IsQuit = true
	u.trayShow = fyne.NewMenuItem("Show", u.showWindow)
	u.trayMenu = fyne.NewMenu("route_after_route", u.trayShow, fyne.NewMenuItemSeparator(), u.trayQuit)
	desk.SetSystemTrayMenu(u.trayMenu)
	if icon := u.app.Icon(); icon != nil {
		desk.SetSystemTrayIcon(icon)
	}
}

// setTrayOpportunities puts one "Accept" entry per pending opportunity at
// the top of the tray menu, to approve without opening the window.
func (u *UI) setTrayOpportunities(ops []live.Opportunity, canTrade bool) {
	if u.trayMenu == nil {
		return
	}
	var items []*fyne.MenuItem
	if canTrade {
		for _, o := range ops {
			id := o.ID
			items = append(items, fyne.NewMenuItem(
				fmt.Sprintf("Accept: %s %s at %s", verbOf(o.Direction), tickerOf(baseOf(o.Symbol)), priceUSD(o.Price)),
				func() { go u.be.Approve(id) }))
		}
		if len(items) > 0 {
			items = append(items, fyne.NewMenuItemSeparator())
		}
	}
	u.trayMenu.Items = append(items, u.trayShow, fyne.NewMenuItemSeparator(), u.trayQuit)
	u.trayMenu.Refresh()
}

func (u *UI) showWindow() {
	u.hidden = false
	u.win.Show()
	u.win.RequestFocus()
}

// onClose hides the window when a tray icon is available, so the algorithm
// keeps running in the background.
func (u *UI) onClose() {
	if u.trayMenu == nil {
		u.doQuit()
		return
	}
	if !u.hidden {
		u.app.SendNotification(fyne.NewNotification("route_after_route keeps running",
			"The algorithm keeps watching your favorites and notifies you of opportunities. Use the tray icon to show the window or quit."))
	}
	u.hidden = true
	u.win.Hide()
}

func (u *UI) doQuit() {
	u.be.Quit()
	u.app.Quit()
}

func (u *UI) setRange(r string) {
	u.settings.Range = r
	u.styleRanges()
	go u.be.SetRange(r)
	u.loadHistory()
	if u.hasSnap {
		u.apply(u.snap)
	}
}

func (u *UI) styleRanges() {
	for r, b := range u.rangeBtns {
		b.Importance = widget.LowImportance
		if r == u.settings.Range {
			b.Importance = widget.HighImportance
		}
		b.Refresh()
	}
}

// loadHistory fetches the long-range curve of the shown crypto, when the
// chart shows a long range.
func (u *UI) loadHistory() {
	pair, rng := u.settings.Pair, u.settings.Range
	if liveRange(rng) {
		return
	}
	key := pair + "/" + rng
	if key != u.longKey {
		u.longKey, u.longPts, u.longErr = key, nil, ""
	}
	u.longAt = timeNow()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		pts, err := u.be.History(ctx, pair, rng)
		fyne.Do(func() {
			if u.longKey != key {
				return // the user moved on
			}
			u.longPts, u.longErr = pts, ""
			if err != nil {
				u.longErr = err.Error()
			}
			if u.hasSnap {
				u.apply(u.snap)
			}
		})
	}()
}

// curve returns the points to draw for the shown crypto and range, or a
// message while they are not available.
func (u *UI) curve(s live.Snapshot) ([]Point, string) {
	pair, rng := u.settings.Pair, u.settings.Range
	if liveRange(rng) {
		pts, _ := pointsFor(s, pair, rng)
		return pts, ""
	}
	key := pair + "/" + rng
	switch {
	case u.longKey == key && u.longPts != nil:
		return withLivePrice(u.longPts, s, pair), ""
	case u.longKey == key && u.longErr != "":
		return nil, "Could not load the curve: " + u.longErr
	case u.demo:
		pts, _ := pointsFor(s, pair, rng) // the replay series is the history
		return pts, ""
	}
	return nil, "Loading the " + rangeInfo[rng].span + "…"
}

func (u *UI) refreshCoinHeader() {
	base := baseOf(u.settings.Pair)
	u.badgeBox.Objects = []fyne.CanvasObject{coinBadge(base, 40)}
	u.badgeBox.Refresh()
	name := coinName(base)
	if name == "" {
		name = base
	}
	u.coinName.Text = name
	u.coinName.Refresh()
	u.coinTicker.Text = tickerOf(base) + " · US dollar"
	u.coinTicker.Refresh()
	if u.leftBox != nil {
		u.leftBox.Refresh()
	}
}

// showPair shows another favorite on the chart.
func (u *UI) showPair(pair string) {
	if pair == u.settings.Pair {
		return
	}
	u.settings.Pair = pair
	u.refreshCoinHeader()
	u.loadHistory()
	if u.hasSnap {
		u.apply(u.snap)
	}
	go func() {
		if err := u.be.SetPair(pair); err != nil {
			fyne.Do(func() { dialog.ShowError(err, u.win) })
		}
	}()
}

func toneColor(t Tone) color.Color {
	switch t {
	case ToneUp:
		return themeColor(colorUp)
	case ToneDown:
		return themeColor(colorDown)
	default:
		return themeColor(colorMuted)
	}
}

func (u *UI) apply(s live.Snapshot) {
	first := !u.hasSnap
	u.snap, u.hasSnap = s, true
	pair := u.settings.Pair
	fg := themeColor(theme.ColorNameForeground)

	// Feed status.
	state, tone := feedState(s)
	u.statusDot.FillColor = toneColor(tone)
	u.statusDot.Refresh()
	u.statusText.Text = fmt.Sprintf("%s · Binance · updated %s", state, clock(timeNow()))
	u.statusText.Refresh()

	// Price and curve.
	k, ok := keyFor(s, pair)
	if u.switching != "" || !ok {
		u.price.Text = "—"
		u.price.Refresh()
		u.changeText.Text, u.changeSpan.Text = "", ""
		u.changeBg.FillColor = color.Transparent
		u.changeText.Refresh()
		u.changeSpan.Refresh()
		u.changeBg.Refresh()
		u.chart.SetMessage("Loading " + baseOf(pair) + "…")
	} else {
		u.price.Text = priceUSD(s.Prices[k])
		u.price.Color = fg
		u.price.Refresh()
		pts, msg := u.curve(s)
		chg := change(pts)
		col := themeColor(colorUp)
		if chg < 0 {
			col = themeColor(colorDown)
		}
		u.changeText.Text, u.changeText.Color = arrowPct(chg), col
		u.changeBg.FillColor = withAlpha(col, 0x22)
		u.changeSpan.Text = "  " + rangeInfo[u.settings.Range].span
		u.changeText.Refresh()
		u.changeBg.Refresh()
		u.changeSpan.Refresh()
		entry := 0.0
		if p, held := botPosition(s, pair); held {
			entry = p.EntryPrice
		}
		if msg != "" {
			u.chart.SetMessage(msg)
		} else {
			u.chart.SetData(pts, markersFor(s, pair), entry)
		}
	}

	// Advice.
	adv := adviceFor(s, pair)
	u.adviceWord.Text, u.adviceWord.Color = adv.Word, toneColor(adv.Tone)
	if adv.Tone == ToneNeutral {
		u.adviceWord.Color = fg
	}
	u.adviceWord.Refresh()
	u.adviceDot.FillColor = toneColor(adv.Tone)
	u.adviceDot.Refresh()
	u.adviceLine.SetText(adv.Line)

	// Bot permission.
	if u.toggle.On != s.Execute {
		u.toggle.SetOn(s.Execute, false)
	}
	u.refreshToggleHint()

	// Ledger (devnet or demo) and positions.
	if u.ledgerMode() {
		u.balance.Text = usdCents(s.Equity)
		u.balance.Refresh()
		u.balancePnl.Text = ""
		if base := s.BaseProfile.Capital; base > 0 && s.State.Cycle > 0 {
			chg := float64(s.Equity-base) / float64(base)
			u.balancePnl.Text = "  " + pct(chg)
			u.balancePnl.Color = toneColor(ToneUp)
			if chg < 0 {
				u.balancePnl.Color = toneColor(ToneDown)
			}
		}
		u.balancePnl.Refresh()
		u.position.SetText(u.positionText(s))
	}

	u.opps.apply(s)
	u.watch.apply(s)
	if u.ledgerMode() {
		u.hist.apply(s)
	}
	u.histView.apply(s)

	// Texts changed width: lay the cards out again.
	u.leftBox.Refresh()
	u.botBox.Refresh()
	u.notify(first)
}

// ledgerMode reports whether the bot has a ledger to trade with (devnet or
// demo). In public mode it only advises.
func (u *UI) ledgerMode() bool { return u.demo || u.settings.WalletMode == ModeDevnet }

// refreshMode shows or hides what depends on the wallet mode.
func (u *UI) refreshMode() {
	if u.ledgerMode() {
		u.toggleRow.Show()
		u.ledgerBox.Show()
		u.hist.card.Show()
	} else {
		u.toggleRow.Hide()
		u.ledgerBox.Hide()
		u.hist.card.Hide()
	}
	u.refreshToggleHint()
	u.botBox.Refresh()
	if u.hasSnap {
		u.apply(u.snap)
	}
}

func (u *UI) positionText(s live.Snapshot) string {
	if len(s.State.Positions) == 0 {
		if !u.demo && u.settings.LedgerCapital == 0 {
			return "Not funded yet: airdrop devnet SOL, then sync the ledger in the wallet card."
		}
		return "No open position."
	}
	var lines []string
	for _, p := range s.State.Positions {
		px, ok := s.Prices[keyOf(p.Market, p.Symbol)]
		pnl := ""
		if ok {
			pnl = " (" + pct(p.PnL(px)) + ")"
		}
		lines = append(lines, fmt.Sprintf("Holding %s of %s for %s at %s%s.",
			usdCents(p.Amount), baseOf(p.Symbol), heldFor(s, p.OpenedAt), priceUSD(p.EntryPrice), pnl))
	}
	return strings.Join(lines, "\n")
}

// heldFor tells how long a position has been held, with the max hold.
func heldFor(s live.Snapshot, opened time.Time) string {
	out := shortDuration(max(s.Now.Sub(opened), 0))
	if s.MaxHold > 0 {
		out += " (closes by " + shortDuration(s.MaxHold) + ")"
	}
	return out
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min %02d s", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func (u *UI) refreshToggleHint() {
	u.toggleHint.Importance = widget.MediumImportance
	switch {
	case !u.ledgerMode():
		u.toggleHint.SetText("Public wallet: the algorithm only advises and notifies you. Switch the wallet card to devnet to let the bot trade.")
	case u.toggle.On:
		hint := "The bot may trade. Each entry waits for your approval; exits (stop-loss"
		if u.snap.MaxHold > 0 {
			hint += ", " + shortDuration(u.snap.MaxHold) + " max hold"
		}
		u.toggleHint.SetText(hint + ", signal change) are automatic.")
	case len(u.snap.State.Positions) > 0:
		u.toggleHint.Importance = widget.WarningImportance
		u.toggleHint.SetText("The bot holds a position but is not allowed to trade: no stop-loss until you allow it again.")
	default:
		u.toggleHint.SetText("The bot won't trade. You are still notified of every opportunity.")
	}
	u.toggleHint.Refresh()
}

func (u *UI) onToggle(on bool) {
	if !on {
		go u.setExecution(false)
		u.refreshToggleHint()
		return
	}
	where := "on the devnet ledger (test SOL, no real money)"
	if u.demo {
		where = "on the demo ledger (no real money)"
	}
	dialog.ShowConfirm("Allow the bot to trade?",
		"The bot will trade "+where+".\n\n"+
			"It never enters a position on its own: every entry is notified and waits\n"+
			"for your approval. Exits (stop-loss, max hold, signal change) are automatic.\n\n"+
			"You can stop it at any time with the same switch.",
		func(ok bool) {
			if !ok {
				u.toggle.SetOn(false, false)
				return
			}
			go u.setExecution(true)
			u.refreshToggleHint()
		}, u.win)
}

func (u *UI) setExecution(on bool) {
	if err := u.be.SetExecution(on); err != nil {
		fyne.Do(func() {
			u.toggle.SetOn(!on, false)
			u.refreshToggleHint()
			dialog.ShowError(err, u.win)
		})
	}
}

// notify sends a desktop notification for each new opportunity (always),
// and for each executed order while the window is hidden.
func (u *UI) notify(first bool) {
	log := u.snap.Log
	start := len(log)
	for i := len(log) - 1; i >= 0; i-- {
		if log[i] == u.lastEvent {
			break
		}
		start = i
	}
	if len(log) > 0 {
		u.lastEvent = log[len(log)-1]
	}
	if first {
		return
	}
	where := "devnet"
	if u.demo {
		where = "demo"
	}
	for _, ev := range log[start:] {
		o := ev.Order
		if o == nil {
			continue
		}
		switch {
		case ev.Kind == live.EventOpportunity:
			body := fmt.Sprintf("%s at %s · %s to accept in route_after_route.", usdCents(o.Amount), priceUSD(o.Price), u.ttlText())
			if !u.snap.Execute {
				body = fmt.Sprintf("At %s. Advice only: the bot is not allowed to trade.", priceUSD(o.Price))
			}
			u.app.SendNotification(fyne.NewNotification(
				fmt.Sprintf("%s: %s opportunity", tickerOf(baseOf(o.Symbol)), verbOf(o.Direction)), body))
		case ev.Kind == live.EventOrder && u.hidden:
			verb := "bought"
			if o.Kind == output.OrderClose {
				verb = "sold"
			}
			u.app.SendNotification(fyne.NewNotification(
				fmt.Sprintf("The bot %s %s", verb, tickerOf(baseOf(o.Symbol))),
				fmt.Sprintf("%s at %s (%s)", usdCents(o.Amount), priceUSD(o.Price), where)))
		}
	}
}

// ttlText is the approval window, e.g. "10 s".
func (u *UI) ttlText() string {
	if n := u.settings.ApprovalSeconds; n > 0 {
		return fmt.Sprintf("%d s", n)
	}
	return "a few seconds"
}

// verbOf names an entry direction.
func verbOf(direction string) string {
	if direction == output.Short {
		return "sell"
	}
	return "buy"
}
