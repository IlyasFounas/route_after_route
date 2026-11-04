package gui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

const pair = "SOLUSDT"

var key = loop.PriceKey{Market: "binance-spot", Symbol: pair}

func series(n int, start, step float64) []market.Bar {
	out := make([]market.Bar, n)
	for i := range out {
		v := start + step*float64(i)
		out[i] = market.Bar{Time: t0.Add(time.Duration(i) * 15 * time.Minute), Open: v, High: v, Low: v, Close: v}
	}
	return out
}

func snapshot(withSignal, held bool, dir string) live.Snapshot {
	bars := series(200, 100, 0.1)
	s := live.Snapshot{
		Now:          bars[len(bars)-1].Time.Add(15 * time.Minute),
		BaseProfile:  profile.Profile{Capital: 1_000_000, Skills: []profile.Skill{}, RiskTolerance: 5},
		Equity:       1_012_300,
		Prices:       loop.Prices{key: bars[len(bars)-1].Close},
		Series:       map[loop.PriceKey][]market.Bar{key: bars},
		Statuses:     map[string]market.StreamStatus{"binance-spot": {State: market.StreamConnected}},
		StrategyInfo: map[string]string{"zscore_revert": "Mean reversion."},
	}
	s.Report.Profile = s.BaseProfile
	if withSignal {
		s.Report.Actions = []output.Action{{Rank: 1, Market: key.Market, Strategy: "zscore_revert", Score: 1.4, Weight: 0.5, Allocation: 500_000,
			Trace:   map[string]float64{"max_drawdown": 0.08, "trades": 120, "costs": 0.03},
			Signals: []output.SymbolSignal{{Symbol: pair, Direction: dir, Exposure: 0.25, Signal: 0.2, Trace: map[string]float64{"z": -1.3}}}}}
	}
	if held {
		s.State = loop.State{Cycle: 3, Positions: []loop.Position{{Market: key.Market, Strategy: "zscore_revert", Symbol: pair, Direction: 1, Amount: 250_000, EntryPrice: 110, OpenedAt: t0}}}
		order := output.Order{Kind: output.OrderOpen, Market: key.Market, Symbol: pair, Direction: output.Long, Amount: 250_000, Price: 110}
		s.Log = []live.Event{{Time: bars[100].Time, Kind: live.EventOrder, Text: "open", Order: &order}}
	}
	return s
}

func TestAdvice(t *testing.T) {
	cases := []struct {
		name         string
		signal, held bool
		dir, want    string
	}{
		{"no strategy", false, false, "", "Wait"},
		{"no strategy but holding", false, true, "", "Sell"},
		{"long, not held", true, false, output.Long, "Buy"},
		{"long, held", true, true, output.Long, "Hold"},
		{"flat, held", true, true, output.Flat, "Sell"},
		{"flat, not held", true, false, output.Flat, "Wait"},
	}
	for _, c := range cases {
		if got := adviceFor(snapshot(c.signal, c.held, c.dir), pair); got.Word != c.want || got.Line == "" {
			t.Errorf("%s: got %q (%q), want %q", c.name, got.Word, got.Line, c.want)
		}
	}
}

func TestPointsAndRange(t *testing.T) {
	s := snapshot(false, false, "")
	day, chg := pointsFor(s, pair, "1D")
	if len(day) != 97 || chg <= 0 { // 24 h of 15-min candles, both ends included
		t.Fatalf("1D: %d points, change %v", len(day), chg)
	}
	if month, _ := pointsFor(s, pair, "1M"); len(month) != 200 {
		t.Fatalf("1M should keep the whole series, got %d", len(month))
	}
	if pts, _ := pointsFor(s, "BTCUSDT", "1D"); pts != nil {
		t.Fatal("unknown pair should give no points")
	}
}

func TestWhyMarkdown(t *testing.T) {
	md := whyMarkdown(snapshot(true, true, output.Long), pair)
	for _, want := range []string{"Hold", "zscore_revert", "Mean reversion.", "Sharpe", "8.0%", "z:", "Recent bot activity", "Bought"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestFormatting(t *testing.T) {
	cases := map[string]string{
		usd(1284.1): "$1,284.10", usd(-5): "-$5.00", usd(1234567.891): "$1,234,567.89",
		priceUSD(67123.456): "$67,123.46", priceUSD(142.3): "$142.30", priceUSD(0.4567): "$0.4567",
		priceUSD(0.00002341): "$0.0000234", priceUSD(0.00000912): "$0.00000912", amount(8.2): "8.20", amount(12345): "12,345", arrowPct(-0.0241): "▼ 2.41%",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	money := map[string]int64{"10000": 1_000_000, "10,000": 1_000_000, "10,000.50": 1_000_050, "10 000,50": 1_000_050, "$250": 25_000, "1,000,000": 100_000_000}
	for in, want := range money {
		if got, err := parseMoney(in); err != nil || got != want {
			t.Errorf("parseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseMoney("abc"); err == nil {
		t.Error("parseMoney should reject text")
	}
}

func TestFilterCoins(t *testing.T) {
	coins := []Coin{{Base: "BTC"}, {Base: "SOL"}, {Base: "SOLV"}, {Base: "PEPE"}}
	if got := filterCoins(coins, "sol"); len(got) != 2 || got[0].Base != "SOL" {
		t.Fatalf("%+v", got)
	}
	if got := filterCoins(coins, "bitco"); len(got) != 1 || got[0].Base != "BTC" {
		t.Fatalf("search by name: %+v", got)
	}
	if got := filterCoins(coins, " "); len(got) != 4 {
		t.Fatal("empty search keeps everything")
	}
}

func TestChartDrawsCurve(t *testing.T) {
	test.NewTempApp(t).Settings().SetTheme(WarmTheme{})
	c := newLineChart()
	pts := make([]Point, 100)
	for i := range pts {
		pts[i] = Point{T: t0.Add(time.Duration(i) * time.Minute), V: 100 + float64(i%10)}
	}
	c.SetData(pts, []Marker{{T: pts[50].T, V: pts[50].V, Kind: "buy"}}, 104)
	r := test.TempWidgetRenderer(t, c).(*lineRenderer)
	r.Layout(fyne.NewSize(600, 300))
	img := r.draw(600, 300).(*image.NRGBA)
	painted := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			painted++
		}
	}
	if painted < 600*20 {
		t.Fatalf("the curve and its fill should cover the plot, %d pixels painted", painted)
	}
	if len(r.Objects()) < 10 {
		t.Fatalf("overlay missing: %d objects", len(r.Objects()))
	}
	c.SetMessage("Loading BTC…")
	if img := r.draw(600, 300).(*image.NRGBA); img.Pix[len(img.Pix)/2+3] != 0 {
		t.Fatal("no curve should be drawn while loading")
	}
}

type fakeBackend struct {
	snaps    chan live.Snapshot
	wallets  chan WalletView
	settings Settings
	demo     bool
	mu       sync.Mutex
	executes []bool
	approved []string
	history  []Point
}

func (f *fakeBackend) calls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.executes)
}

func (f *fakeBackend) approvals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.approved)
}

func (f *fakeBackend) Snapshots() <-chan live.Snapshot { return f.snaps }
func (f *fakeBackend) Wallets() <-chan WalletView      { return f.wallets }
func (f *fakeBackend) Settings() Settings              { return f.settings }
func (f *fakeBackend) Demo() bool                      { return f.demo }
func (f *fakeBackend) SetRange(string)                 {}
func (f *fakeBackend) SetExecution(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executes = append(f.executes, on)
	return nil
}
func (f *fakeBackend) SetPair(string) error                  { return nil }
func (f *fakeBackend) SetFavorites([]string) error           { return nil }
func (f *fakeBackend) Coins(context.Context) ([]Coin, error) { return nil, nil }
func (f *fakeBackend) Logo(context.Context, string) ([]byte, error) {
	return nil, errors.New("offline")
}
func (f *fakeBackend) History(context.Context, string, string) ([]Point, error) {
	return f.history, nil
}
func (f *fakeBackend) Approve(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = append(f.approved, id)
}
func (f *fakeBackend) Reject(string)                {}
func (f *fakeBackend) SetWalletMode(string) error   { return nil }
func (f *fakeBackend) SetWallet(string) error       { return nil }
func (f *fakeBackend) Airdrop() error               { return nil }
func (f *fakeBackend) SyncLedger() error            { return nil }
func (f *fakeBackend) RefreshWallet()               {}
func (f *fakeBackend) SetRiskTolerance(int) error   { return nil }
func (f *fakeBackend) SetTiming(int, int) error     { return nil }
func (f *fakeBackend) SetRPCs(string, string) error { return nil }
func (f *fakeBackend) Folder() string               { return "/tmp/rar" }
func (f *fakeBackend) Quit()                        {}

func newTestUI(t *testing.T) (*UI, *fakeBackend) {
	return newTestUIWith(t, Settings{Pair: pair, Favorites: []string{pair, "BTCUSDT"}, Range: "1D",
		WalletMode: ModeDevnet, LedgerCapital: 1_000_000, ApprovalSeconds: 10, MaxHoldSeconds: 60})
}

func newTestUIWith(t *testing.T, st Settings) (*UI, *fakeBackend) {
	a := test.NewTempApp(t)
	be := &fakeBackend{snaps: make(chan live.Snapshot), wallets: make(chan WalletView), settings: st}
	return New(a, be), be
}

func TestApplySnapshot(t *testing.T) {
	u, _ := newTestUI(t)
	if u.coinName.Text != "Solana" || u.coinTicker.Text != "$SOL · US dollar" {
		t.Fatalf("header %q / %q", u.coinName.Text, u.coinTicker.Text)
	}
	s := snapshot(true, true, output.Long)
	u.apply(s)
	if u.price.Text != "$119.90" || !strings.HasPrefix(u.changeText.Text, "▲") {
		t.Fatalf("price %q change %q", u.price.Text, u.changeText.Text)
	}
	u.longKey, u.longPts = pair+"/1D", []Point{{T: t0, V: 100}, {T: t0.Add(time.Hour), V: 110}}
	u.apply(s)
	if u.adviceWord.Text != "Hold" || u.balance.Text != "$10,123.00" || u.balancePnl.Text != "  +1.23%" {
		t.Fatalf("advice %q balance %q pnl %q", u.adviceWord.Text, u.balance.Text, u.balancePnl.Text)
	}
	if !strings.Contains(u.position.Text, "Holding $2,500.00 of SOL for") {
		t.Fatalf("position %q", u.position.Text)
	}
	if !strings.Contains(u.toggleHint.Text, "no stop-loss") {
		t.Fatalf("a held position without permission must be flagged: %q", u.toggleHint.Text)
	}
	if !strings.HasPrefix(u.statusText.Text, "Live") {
		t.Fatalf("status %q", u.statusText.Text)
	}
	s.Execute = true
	u.apply(s)
	if !u.toggle.On || !strings.Contains(u.toggleHint.Text, "waits for your approval") {
		t.Fatalf("toggle should follow the engine: on=%v hint %q", u.toggle.On, u.toggleHint.Text)
	}
	u.switching = "BTCUSDT"
	u.apply(s)
	if u.price.Text != "—" {
		t.Fatal("while switching, the old price must not be shown")
	}
}

func TestTurningTheBotOffNeedsNoConfirmation(t *testing.T) {
	u, be := newTestUI(t)
	u.apply(snapshot(false, false, ""))
	u.toggle.SetOn(true, false)
	u.onToggle(false)
	deadline := time.Now().Add(2 * time.Second)
	for len(be.calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := be.calls(); len(got) != 1 || got[0] {
		t.Fatalf("SetExecution calls = %v", got)
	}
}

func TestWalletCard(t *testing.T) {
	u, _ := newTestUIWith(t, Settings{Pair: pair, Favorites: []string{pair}, Range: "1D", WalletMode: ModePublic})
	if len(u.walletBox.Objects) != 4 { // mode switch + title, intro, import
		t.Fatalf("empty state should show the import invitation, got %d objects", len(u.walletBox.Objects))
	}
	u.applyWallet(WalletView{Address: "11111111111111111111111111111111", Updated: t0, Total: 1284.1, Holdings: []HoldingView{
		{Holding: wallet.Holding{Symbol: "SOL", Name: "Solana", Amount: 8.2}, Price: 142.37, Value: 1167.43, Priced: true},
		{Holding: wallet.Holding{Symbol: "AbCd…WxYz", Amount: 3}},
	}})
	found := false
	for _, o := range u.walletBox.Objects {
		if tx, ok := o.(interface{ MinSize() fyne.Size }); ok && tx != nil {
			found = true
		}
	}
	if !found || len(u.walletBox.Objects) < 7 {
		t.Fatalf("wallet rows missing: %d objects", len(u.walletBox.Objects))
	}
}

func TestReadTiming(t *testing.T) {
	ap, mh, err := readTiming(" 10 ", "45")
	if err != nil || ap != 10 || mh != 45 {
		t.Fatalf("%d %d %v", ap, mh, err)
	}
	for _, bad := range [][2]string{{"1", "45"}, {"10", "2"}, {"x", "45"}, {"10", "9999"}} {
		if _, _, err := readTiming(bad[0], bad[1]); err == nil {
			t.Errorf("%v should be rejected", bad)
		}
	}
}

// secondSnapshot is a live snapshot of 1-second candles with one opportunity.
func secondSnapshot() live.Snapshot {
	s := snapshot(true, false, output.Long)
	bars := make([]market.Bar, 600)
	for i := range bars {
		v := 100 + 0.01*float64(i)
		bars[i] = market.Bar{Time: t0.Add(time.Duration(i) * time.Second), Open: v, High: v, Low: v, Close: v}
	}
	s.Series[key] = bars
	s.Prices[key] = bars[len(bars)-1].Close
	s.Now = bars[len(bars)-1].Time.Add(time.Second)
	s.MaxHold = time.Minute
	o := output.Order{Kind: output.OrderProposal, Market: key.Market, Strategy: "zscore_revert", Symbol: pair, Direction: output.Long, Amount: 250_000, Price: 105.99}
	s.Log = append(s.Log, live.Event{Time: s.Now, Kind: live.EventOpportunity, Order: &o})
	s.Opportunities = []live.Opportunity{{ID: "SOLUSDT-1", Market: key.Market, Strategy: "zscore_revert", Symbol: pair,
		Direction: output.Long, Signal: 0.8, Price: 105.99, Amount: 250_000, At: t0, Expires: timeNow().Add(8 * time.Second)}}
	return s
}

func TestSecondRanges(t *testing.T) {
	s := secondSnapshot()
	for rng, want := range map[string]int{"1min": 61, "5min": 301, "1h": 600} {
		if pts, _ := pointsFor(s, pair, rng); len(pts) != want {
			t.Errorf("%s: %d points, want %d", rng, len(pts), want)
		}
	}
	if !liveRange("1h") || liveRange("1D") || !ValidRange("5min") || ValidRange("15min") {
		t.Fatal("range classification")
	}
	if ms := markersFor(s, pair); len(ms) != 0 {
		t.Fatalf("an opportunity is not an executed order: %+v", ms)
	}
	long := withLivePrice([]Point{{T: t0.Add(-time.Hour), V: 99}}, s, pair)
	if len(long) != 2 || long[1].V != s.Prices[key] {
		t.Fatalf("long curve must end at the live price: %+v", long)
	}
}

func TestOpportunityCard(t *testing.T) {
	u, be := newTestUI(t)
	s := secondSnapshot()
	s.Execute = true
	u.apply(s)
	r := u.opps.rows["SOLUSDT-1"]
	if r == nil || r.accept.Disabled() || !strings.HasSuffix(r.countdown.Text, "s left") {
		t.Fatalf("opportunity row missing or not actionable: %+v", r)
	}
	test.Tap(r.accept)
	deadline := time.Now().Add(2 * time.Second)
	for len(be.approvals()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := be.approvals(); len(got) != 1 || got[0] != "SOLUSDT-1" {
		t.Fatalf("approvals = %v", got)
	}
	// Refreshing with the same opportunity keeps the same row (no lost click).
	u.apply(s)
	if u.opps.rows["SOLUSDT-1"] != r {
		t.Fatal("rows must not be rebuilt while the opportunities are unchanged")
	}
	// Expired: no longer accepted.
	u.opps.tick(timeNow().Add(time.Minute))
	if r.countdown.Text != "expired" || !r.accept.Disabled() {
		t.Fatalf("countdown %q", r.countdown.Text)
	}
	// Watchlist row says it.
	if st := u.watch.rows[pair].state.Text; st != "buy opportunity" {
		t.Fatalf("watch state %q", st)
	}
}

func TestPublicModeOnlyAdvises(t *testing.T) {
	u, _ := newTestUIWith(t, Settings{Pair: pair, Favorites: []string{pair}, Range: "5min", WalletMode: ModePublic})
	s := secondSnapshot()
	u.apply(s)
	if u.toggleRow.Visible() || u.ledgerBox.Visible() {
		t.Fatal("public mode must not offer trading nor show a ledger")
	}
	if !strings.Contains(u.toggleHint.Text, "only advises") {
		t.Fatalf("hint %q", u.toggleHint.Text)
	}
	if r := u.opps.rows["SOLUSDT-1"]; r == nil || len(u.opps.box.Objects) == 0 {
		t.Fatal("opportunities are still shown as advice")
	}
	for _, o := range u.opps.box.Objects {
		if b, ok := o.(*fyne.Container); ok {
			for _, x := range b.Objects {
				if x == u.opps.rows["SOLUSDT-1"].accept {
					t.Fatal("no Accept button in public mode")
				}
			}
		}
	}
}

func TestDevnetWalletCard(t *testing.T) {
	u, _ := newTestUI(t)
	u.applyWallet(WalletView{Devnet: true, Address: "11111111111111111111111111111111", Updated: t0, Total: 150,
		Holdings: []HoldingView{{Holding: wallet.Holding{Symbol: "SOL", Name: "Solana", Amount: 1}, Price: 150, Value: 150, Priced: true}}})
	var labels []string
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch x := o.(type) {
		case *widget.Button:
			labels = append(labels, x.Text)
		case *fyne.Container:
			for _, c := range x.Objects {
				walk(c)
			}
		}
	}
	walk(u.walletBox)
	for _, want := range []string{"Public wallet", "Devnet", "Airdrop 1 SOL", "Sync ledger", "Copy address"} {
		if !slices.Contains(labels, want) {
			t.Errorf("missing button %q in %v", want, labels)
		}
	}
}

func sampleTrades() []loop.Trade {
	return []loop.Trade{
		{Market: key.Market, Symbol: pair, Direction: 1, Amount: 25_000, EntryPrice: 100, ExitPrice: 101, OpenedAt: t0, ClosedAt: t0.Add(30 * time.Second), Fees: 75, PnL: 175, Reason: "max hold reached: 1m0s (+1.00%)"},
		{Market: key.Market, Symbol: "BTCUSDT", Direction: 1, Amount: 50_000, EntryPrice: 60_000, ExitPrice: 59_940, OpenedAt: t0, ClosedAt: t0.Add(12 * time.Second), Fees: 150, PnL: -200, Reason: "signal direction changed"},
		{Market: key.Market, Symbol: pair, Direction: 1, Amount: 10_000, EntryPrice: 100, ExitPrice: 100.3, OpenedAt: t0, ClosedAt: t0.Add(45 * time.Second), Fees: 30, PnL: 0, Reason: "signal direction changed"},
	}
}

func TestTradeSummary(t *testing.T) {
	s := summarize(sampleTrades())
	if s.Trades != 3 || s.Won != 1 || s.Lost != 1 || s.PnL != -25 || s.Fees != 255 || s.Best.PnL != 175 || s.Worst.PnL != -200 {
		t.Fatalf("%+v", s)
	}
	line := summaryLine(s)
	for _, want := range []string{"3 trades", "1 won, 1 lost", "33% won", "net −$0.25", "fees $2.55"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q misses %q", line, want)
		}
	}
	if summaryLine(summarize(nil)) != "No closed trade yet." {
		t.Fatal("empty history")
	}
	tr := sampleTrades()[0]
	if got := tradeTitle(tr); got != "$SOL long · +$1.75 (+0.70%)" {
		t.Fatalf("title %q", got)
	}
	if d := tradeDetail(tr); !strings.Contains(d, "(30 s)") || !strings.Contains(d, "in at $100.00, out at $101.00") || !strings.Contains(d, "fees $0.75") {
		t.Fatalf("detail %q", d)
	}
}

func TestHistoryCard(t *testing.T) {
	u, _ := newTestUI(t)
	s := snapshot(false, false, "")
	s.State.Trades = sampleTrades()
	u.apply(s)
	if !u.hist.card.Visible() || len(u.hist.box.Objects) != historyRows || !strings.Contains(u.hist.summary.Text, "3 trades") {
		t.Fatalf("history card: %d rows, %q", len(u.hist.box.Objects), u.hist.summary.Text)
	}
	// Newest first.
	first := u.hist.box.Objects[0].(*fyne.Container).Objects[0].(*fyne.Container).Objects[0].(*widget.Label)
	if !strings.HasPrefix(first.Text, "$SOL long · $0.00") {
		t.Fatalf("first row %q", first.Text)
	}
	u.show(screenHistory)
	if !u.histView.content.Visible() || len(u.histView.trades) != 3 || u.histView.trades[0].PnL != 0 || u.histView.empty.Visible() {
		t.Fatalf("history screen: %d trades", len(u.histView.trades))
	}

	pub, _ := newTestUIWith(t, Settings{Pair: pair, Favorites: []string{pair}, Range: "5min", WalletMode: ModePublic})
	pub.apply(s)
	if pub.hist.card.Visible() {
		t.Fatal("no ledger, no history in public mode")
	}
}

func TestMatchCommand(t *testing.T) {
	for in, want := range map[string]string{
		"trading": "trading", " Trading ": "trading", "t": "trading", "algo": "trading",
		"historic": "historic", "history": "historic", "hist": "historic", "H": "historic",
	} {
		if c, ok := MatchCommand(in); !ok || c.Name != want {
			t.Errorf("%q → %q %v, want %q", in, c.Name, ok, want)
		}
	}
	for _, in := range []string{"", "  ", "buy", "tradingx"} {
		if c, ok := MatchCommand(in); ok {
			t.Errorf("%q should not match, got %q", in, c.Name)
		}
	}
}

func TestHomePrompt(t *testing.T) {
	u, _ := newTestUI(t)
	if u.screen != screenHome || !u.screens[screenHome].Visible() || u.screens[screenTrading].Visible() {
		t.Fatal("the app opens on the prompt")
	}
	test.Type(u.prompt, "hist")
	if !strings.HasPrefix(u.promptHint.Text, "historic — ") {
		t.Fatalf("hint %q", u.promptHint.Text)
	}
	u.prompt.OnSubmitted(u.prompt.Text)
	if u.screen != screenHistory || !u.screens[screenHistory].Visible() || u.screens[screenHome].Visible() {
		t.Fatal("historic should open the history")
	}
	u.onKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if u.screen != screenHome || u.prompt.Text != "" {
		t.Fatal("Escape goes back to the prompt, emptied")
	}
	if u.Open("sell everything") || u.screen != screenHome || !strings.Contains(u.promptHint.Text, "is not a command") {
		t.Fatalf("unknown command: %q", u.promptHint.Text)
	}
	if !u.Open("trading") || !u.screens[screenTrading].Visible() {
		t.Fatal("trading should open the algorithm")
	}
}

func TestTickerSearch(t *testing.T) {
	coins := []Coin{{Pair: "SOLUSDT", Base: "SOL"}, {Pair: "SUSHIUSDT", Base: "SUSHI"}, {Pair: "BTCUSDT", Base: "BTC"}}
	if got := filterCoins(coins, "$so"); len(got) != 1 || got[0].Base != "SOL" {
		t.Fatalf("$so → %+v", got)
	}
	if got := filterCoins(coins, "$S"); len(got) != 2 {
		t.Fatalf("$S → %+v", got)
	}
	if got := filterCoins(coins, "bitcoin"); len(got) != 1 {
		t.Fatalf("by name → %+v", got)
	}
	if watchName("SOLUSDT") != "$SOL · Solana" || watchName("XYZUSDT") != "$XYZ" {
		t.Fatal(watchName("SOLUSDT"))
	}
}

func TestLogos(t *testing.T) {
	// A square opaque logo becomes a disc: corners transparent, centre kept.
	sq := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range sq.Pix {
		sq.Pix[i] = 0xFF
	}
	r := roundLogo(sq).(*image.NRGBA)
	if r.NRGBAAt(0, 0).A != 0 || r.NRGBAAt(32, 32).A != 0xFF || r.NRGBAAt(32, 0).A == 0 {
		t.Fatalf("corner %v centre %v edge %v", r.NRGBAAt(0, 0), r.NRGBAAt(32, 32), r.NRGBAAt(32, 0))
	}
	// The store swaps the logo into the badges on screen, fetching once.
	test.NewTempApp(t)
	var buf bytes.Buffer
	png.Encode(&buf, sq)
	var mu sync.Mutex
	calls := 0
	st := newLogoStore(func(_ context.Context, base string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return buf.Bytes(), nil
	})
	ui := make(chan func(), 1)
	st.onUI = func(f func()) { ui <- f }
	b1 := st.badge("SOL", 32, letterBadge("SOL", 32)).(*fyne.Container)
	b2 := st.badge("SOL", 20, letterBadge("SOL", 20)).(*fyne.Container)
	select {
	case f := <-ui:
		f() // the "Fyne goroutine" is this one
	case <-time.After(2 * time.Second):
		t.Fatal("logo not delivered")
	}
	_, ok2 := b2.Objects[0].(*canvas.Image)
	if _, ok := b1.Objects[0].(*canvas.Image); !ok || !ok2 {
		t.Fatal("the logo should replace the letter badge")
	}
	if img := st.badge("SOL", 40, letterBadge("SOL", 40)).(*fyne.Container).Objects[0]; img.MinSize().Width != 40 {
		t.Fatal("cached logo at the asked size")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("fetched %d times", calls)
	}
	if validLogoTicker("AbCd…WxYz") || !validLogoTicker("1000SATS") {
		t.Fatal("only plain tickers have logos")
	}
}
