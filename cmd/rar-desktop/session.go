package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	rar "github.com/IlyasFounas/route_after_route/internal/app"
	"github.com/IlyasFounas/route_after_route/internal/gui"
	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/market/binance"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

const (
	defaultPair = "SOLUSDT"
	// algoInterval: trades last a few tens of seconds, so the algorithm
	// works on 1-second candles.
	algoInterval = market.Second
	// liveLookback is the history evaluated by the walk-forward (7200
	// candles of 1 s per crypto: 8 REST pages each at startup).
	liveLookback    = 2 * time.Hour
	liveReevaluate  = 5 * time.Minute
	liveHistoryBars = 3600 // 1 h on the chart
	defaultApproval = 10
	defaultMaxHold  = 60
	walletEvery     = 2 * time.Minute
	coinsCacheFor   = 2 * time.Minute
	historyCacheFor = time.Minute
	// minLedger is the smallest devnet ledger the bot accepts (Binance's
	// minimum notional, in cents).
	minLedger = 1000
)

func defaultSettings() gui.Settings {
	return gui.Settings{Pair: defaultPair, Favorites: []string{defaultPair}, Range: gui.DefaultRange,
		WalletMode: gui.ModePublic, ApprovalSeconds: defaultApproval, MaxHoldSeconds: defaultMaxHold}
}

// paths are the user's files.
type paths struct {
	Dir, Profile, Config, Settings string
	// State is the ledger: the devnet ledger in live mode, the demo ledger
	// with --demo / --config.
	State string
	// DevnetKey is the app-generated devnet keypair (Solana CLI format).
	DevnetKey string
}

// session owns the live engine and everything the window asks for. It
// implements gui.Backend.
type session struct {
	paths paths
	// fixed: the markets come from the config file as is (custom config or
	// demo); the shown crypto is picked among them, without restart.
	fixed bool

	mu         sync.Mutex
	cfg        rar.Config
	profile    profile.Profile // as in profile.json
	settings   gui.Settings
	devnetAddr string
	eng        *live.Engine
	cancel     context.CancelFunc
	done       chan struct{}
	last       live.Snapshot
	coins      []gui.Coin
	coinsAt    time.Time
	history    map[string]historyEntry

	snaps      chan live.Snapshot
	wallets    chan gui.WalletView
	walletKick chan struct{}
	pub        *binance.Public
	ctx        context.Context
	stopAll    context.CancelFunc
}

type historyEntry struct {
	at  time.Time
	pts []gui.Point
}

var _ gui.Backend = (*session)(nil)

func newSession(p paths, fixed bool, pair string) (*session, error) {
	prof, err := profile.Load(p.Profile)
	if err != nil {
		return nil, err
	}
	cfg, err := rar.LoadConfig(p.Config)
	if err != nil {
		return nil, err
	}
	st := defaultSettings()
	if !fixed {
		if st, err = loadSettings(p.Settings); err != nil {
			return nil, err
		}
	}
	if pair != "" {
		pair = strings.ToUpper(pair)
		st.Pair = pair
		if !fixed && !slices.Contains(st.Favorites, pair) {
			if len(st.Favorites) >= gui.MaxFavorites {
				st.Favorites = st.Favorites[:gui.MaxFavorites-1]
			}
			st.Favorites = append(st.Favorites, pair)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		paths: p, fixed: fixed, cfg: cfg, profile: prof, settings: st, history: map[string]historyEntry{},
		snaps: make(chan live.Snapshot, 1), wallets: make(chan gui.WalletView, 1),
		walletKick: make(chan struct{}, 1), pub: binance.NewPublic(), ctx: ctx, stopAll: cancel,
	}
	if !fixed && st.WalletMode == gui.ModeDevnet {
		if err := s.loadDevnetKey(); err != nil {
			cancel()
			return nil, err
		}
	}
	return s, nil
}

// loadSettings reads settings.json, falling back to defaults for anything
// missing or invalid.
func loadSettings(path string) (gui.Settings, error) {
	st := defaultSettings()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Favorites = nil
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	return sanitize(st), nil
}

// sanitize repairs settings edited by hand or written by an older version.
func sanitize(st gui.Settings) gui.Settings {
	var favs []string
	for _, f := range st.Favorites {
		f = strings.ToUpper(strings.TrimSpace(f))
		if validPair(f) && !slices.Contains(favs, f) && len(favs) < gui.MaxFavorites {
			favs = append(favs, f)
		}
	}
	if !validPair(st.Pair) {
		st.Pair = defaultPair
		if len(favs) > 0 {
			st.Pair = favs[0]
		}
	}
	if !slices.Contains(favs, st.Pair) {
		if len(favs) >= gui.MaxFavorites {
			st.Pair = favs[0]
		} else {
			favs = append([]string{st.Pair}, favs...)
		}
	}
	st.Favorites = favs
	if !gui.ValidRange(st.Range) {
		st.Range = gui.DefaultRange
	}
	if st.WalletMode != gui.ModeDevnet {
		st.WalletMode = gui.ModePublic
	}
	if st.Wallet != "" && wallet.ValidateSolanaAddress(st.Wallet) != nil {
		st.Wallet = ""
	}
	if st.ApprovalSeconds < gui.MinApprovalSeconds || st.ApprovalSeconds > gui.MaxApprovalSeconds {
		st.ApprovalSeconds = defaultApproval
	}
	if st.MaxHoldSeconds < gui.MinMaxHoldSeconds || st.MaxHoldSeconds > gui.MaxMaxHoldSeconds {
		st.MaxHoldSeconds = defaultMaxHold
	}
	if st.LedgerCapital < 0 {
		st.LedgerCapital = 0
	}
	return st
}

func validPair(p string) bool { return strings.HasSuffix(p, "USDT") && len(p) > 4 }

func (s *session) saveSettingsLocked() error {
	if s.fixed {
		return nil
	}
	raw, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.paths.Settings, append(raw, '\n'), 0o644)
}

func (s *session) loadDevnetKey() error {
	kp, err := wallet.LoadOrCreateKeypair(s.paths.DevnetKey)
	if err != nil {
		return fmt.Errorf("devnet wallet: %w", err)
	}
	s.devnetAddr = kp.Address()
	return nil
}

// marketID is the market of one watched crypto: one Binance market (one
// stream, one goroutine, one strategy selection) per favorite.
func marketID(pair string) string { return "binance-spot:" + pair }

// engineConfig returns the engine configuration: the config file, with one
// Binance market of 1-second candles per favorite (unless fixed).
func (s *session) engineConfig() rar.Config {
	cfg := s.cfg
	if s.fixed {
		return cfg
	}
	cfg.Markets = make([]rar.MarketConfig, len(s.settings.Favorites))
	for i, f := range s.settings.Favorites {
		opts := fmt.Sprintf(`{"symbols":[%q],"interval":%q,"id":%q}`, f, algoInterval, marketID(f))
		cfg.Markets[i] = rar.MarketConfig{Type: "binance", Options: json.RawMessage(opts)}
	}
	cfg.Period = rar.PeriodConfig{Lookback: rar.Duration{Duration: liveLookback}}
	cfg.ReevaluateEvery = rar.Duration{Duration: liveReevaluate}
	return cfg
}

// canTradeLocked tells why the bot may not trade, if it may not.
func (s *session) canTradeLocked() error {
	switch {
	case s.fixed:
		return nil
	case s.settings.WalletMode != gui.ModeDevnet:
		return errors.New("the public wallet is read-only: switch the wallet card to devnet to let the bot trade")
	case s.settings.LedgerCapital < minLedger:
		return errors.New("the devnet ledger is not funded: airdrop devnet SOL, then press \"Sync ledger\" in the wallet card")
	}
	return nil
}

// ledgerLocked reports whether the engine keeps a ledger (devnet or demo).
func (s *session) ledgerLocked() bool { return s.fixed || s.settings.WalletMode == gui.ModeDevnet }

// engineProfileLocked is the profile given to the engine: in devnet, the
// capital is the ledger's starting value.
func (s *session) engineProfileLocked() profile.Profile {
	p := s.profile
	if !s.fixed && s.settings.WalletMode == gui.ModeDevnet && s.settings.LedgerCapital >= minLedger {
		p.Capital = s.settings.LedgerCapital
	}
	return p
}

// focusLocked is the series published in full: the crypto on the chart.
func (s *session) focusLocked(markets []market.Market) loop.PriceKey {
	for _, m := range markets {
		if d := m.Descriptor(); d.HasSymbol(s.settings.Pair) {
			return loop.PriceKey{Market: d.ID, Symbol: s.settings.Pair}
		}
	}
	return loop.PriceKey{}
}

// start launches the engine and the wallet loop.
func (s *session) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startLocked(); err != nil {
		return err
	}
	go s.walletLoop()
	return nil
}

func (s *session) startLocked() error {
	cfg := s.engineConfig()
	markets, err := cfg.OpenMarkets()
	if err != nil {
		return err
	}
	if s.fixed && !hasSymbol(markets, s.settings.Pair) {
		for _, m := range markets {
			if syms := m.Descriptor().Symbols; len(syms) > 0 {
				s.settings.Pair = syms[0].Name
				break
			}
		}
	}
	strats, err := cfg.OpenStrategies()
	if err != nil {
		return err
	}
	opts := live.Options{
		Profile: s.engineProfileLocked(), Config: cfg, Markets: markets, Strategies: strats,
		Execute:     s.settings.Execute && s.canTradeLocked() == nil,
		HistoryBars: 3000,
		Focus:       s.focusLocked(markets),
		ApprovalTTL: time.Duration(s.settings.ApprovalSeconds) * time.Second,
	}
	if !s.fixed {
		// Every reliable (market, strategy) pair may signal: each favorite
		// is watched, entries are approved one by one anyway.
		opts.Config.Policy.MaxActive = max(cfg.Policy.MaxActive, len(markets)*len(strats))
		opts.HistoryBars = liveHistoryBars
		// Engine time follows the 1 s candles, so the max hold is in real
		// seconds. (The demo replays days in a fraction of a second.)
		opts.MaxHold = time.Duration(s.settings.MaxHoldSeconds) * time.Second
	}
	if s.ledgerLocked() {
		state, err := loop.Load(s.paths.State)
		if err != nil {
			return err
		}
		opts.State = settleForeign(state, markets, s.last.Prices, live.CostBps(markets, cfg))
		opts.StatePath = s.paths.State
	}
	eng, err := live.New(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	ch, unsub := eng.Subscribe()
	go func() {
		defer close(done)
		if err := eng.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "engine:", err)
		}
	}()
	go func() {
		defer unsub()
		for {
			select {
			case snap := <-ch:
				s.mu.Lock()
				s.last = snap
				s.mu.Unlock()
				publish(s.snaps, snap)
			case <-done:
				return
			}
		}
	}()
	s.eng, s.cancel, s.done = eng, cancel, done
	return nil
}

func (s *session) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}
}

// restartLocked applies new settings to the engine; on failure, prev is
// restored.
func (s *session) restartLocked(prev gui.Settings) error {
	s.stopLocked()
	if err := s.startLocked(); err != nil {
		s.settings = prev
		if err2 := s.startLocked(); err2 != nil {
			return errors.Join(err, err2)
		}
		return err
	}
	return s.saveSettingsLocked()
}

func hasSymbol(ms []market.Market, sym string) bool {
	for _, m := range ms {
		if m.Descriptor().HasSymbol(sym) {
			return true
		}
	}
	return false
}

// publish sends v, replacing an unread older value.
func publish[T any](ch chan T, v T) {
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}

func (s *session) Snapshots() <-chan live.Snapshot { return s.snaps }
func (s *session) Wallets() <-chan gui.WalletView  { return s.wallets }
func (s *session) Folder() string                  { return s.paths.Dir }
func (s *session) Demo() bool                      { return s.fixed }

func (s *session) Settings() gui.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.settings
	st.Favorites = slices.Clone(st.Favorites)
	if s.fixed && s.eng != nil {
		// The demo watches the config's markets.
		st.Favorites = nil
		for _, m := range s.eng.Markets() {
			for _, sym := range m.Descriptor().Symbols {
				st.Favorites = append(st.Favorites, sym.Name)
			}
		}
	}
	return st
}

func (s *session) SetRange(r string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Range = r
	_ = s.saveSettingsLocked()
}

func (s *session) SetExecution(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		if err := s.canTradeLocked(); err != nil {
			return err
		}
	}
	s.settings.Execute = on
	s.eng.SetExecution(on)
	return s.saveSettingsLocked()
}

func (s *session) Approve(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eng.Approve(id)
}

func (s *session) Reject(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eng.Reject(id)
}

// SetPair shows pair on the chart. A crypto that is not a favorite yet is
// added to the watchlist, which restarts the engine.
func (s *session) SetPair(pair string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fixed || slices.Contains(s.settings.Favorites, pair) {
		s.settings.Pair = pair
		s.eng.SetFocus(s.focusLocked(s.eng.Markets()))
		return s.saveSettingsLocked()
	}
	if !validPair(pair) {
		return fmt.Errorf("unsupported pair %q", pair)
	}
	if len(s.settings.Favorites) >= gui.MaxFavorites {
		return fmt.Errorf("you already watch %d cryptos: remove one from the watchlist first", gui.MaxFavorites)
	}
	prev := s.settings
	prev.Favorites = slices.Clone(prev.Favorites)
	s.settings.Favorites = append(s.settings.Favorites, pair)
	s.settings.Pair = pair
	return s.restartLocked(prev)
}

// SetFavorites replaces the watched cryptos. Positions on the cryptos no
// longer watched are closed at their last price.
func (s *session) SetFavorites(pairs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fixed {
		return errors.New("the demo watches the markets of its config file")
	}
	var favs []string
	for _, p := range pairs {
		if validPair(p) && !slices.Contains(favs, p) {
			favs = append(favs, p)
		}
	}
	switch {
	case len(favs) == 0:
		return errors.New("keep at least one crypto in the watchlist")
	case len(favs) > gui.MaxFavorites:
		return fmt.Errorf("at most %d cryptos can be watched at once", gui.MaxFavorites)
	}
	prev := s.settings
	prev.Favorites = slices.Clone(prev.Favorites)
	s.settings.Favorites = favs
	if !slices.Contains(favs, s.settings.Pair) {
		s.settings.Pair = favs[0]
	}
	return s.restartLocked(prev)
}

// settleForeign closes the positions held on markets that are no longer
// followed: without a price feed they could never be managed. They are
// closed at their last known price (entry price when unknown), net of costs
// when the market's cost is known.
func settleForeign(st loop.State, markets []market.Market, prices loop.Prices, costs map[string]float64) loop.State {
	ids := map[string]bool{}
	for _, m := range markets {
		ids[m.Descriptor().ID] = true
	}
	var keep, foreign []loop.Position
	for _, p := range st.Positions {
		if ids[p.Market] {
			keep = append(keep, p)
		} else {
			foreign = append(foreign, p)
		}
	}
	if len(foreign) == 0 {
		return st
	}
	settled, _ := loop.CloseAll(loop.State{Cycle: st.Cycle, Cash: st.Cash, Positions: foreign, Trades: st.Trades},
		prices, loop.Rules{CostBps: costs}, "crypto no longer watched", time.Now().UTC())
	st.Cash, st.Positions, st.Trades = settled.Cash, keep, settled.Trades
	return st
}

func baseTicker(pair string) string { return strings.TrimSuffix(pair, "USDT") }

func (s *session) Coins(ctx context.Context) ([]gui.Coin, error) {
	s.mu.Lock()
	if s.fixed {
		defer s.mu.Unlock()
		var out []gui.Coin
		for _, d := range s.last.Markets {
			for _, sym := range d.Symbols {
				if px, ok := s.last.Prices[loop.PriceKey{Market: d.ID, Symbol: sym.Name}]; ok {
					out = append(out, gui.Coin{Pair: sym.Name, Base: sym.Name, Price: px})
				}
			}
		}
		return out, nil
	}
	if time.Since(s.coinsAt) < coinsCacheFor && len(s.coins) > 0 {
		defer s.mu.Unlock()
		return s.coins, nil
	}
	s.mu.Unlock()
	ts, err := s.pub.USDTTickers(ctx)
	if err != nil {
		return nil, err
	}
	coins := make([]gui.Coin, len(ts))
	for i, t := range ts {
		coins[i] = gui.Coin{Pair: t.Symbol, Base: t.Base, Price: t.Last, Change: t.Change}
	}
	s.mu.Lock()
	s.coins, s.coinsAt = coins, time.Now()
	s.mu.Unlock()
	return coins, nil
}

// Logo returns the logo of a crypto: from the disk cache (<config>/logos),
// else downloaded from Binance and cached. Logos are display only: any
// failure just keeps the letter badge.
func (s *session) Logo(ctx context.Context, base string) ([]byte, error) {
	if !binance.ValidTicker(base) {
		return nil, fmt.Errorf("invalid ticker %q", base)
	}
	path := filepath.Join(s.paths.Dir, "logos", base+".png")
	if raw, err := os.ReadFile(path); err == nil {
		return raw, nil
	}
	raw, err := s.pub.Logo(ctx, base)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, raw, 0o644)
	}
	return raw, nil
}

// historyCandles are the display candles of the long chart ranges.
var historyCandles = map[string]struct {
	interval market.Granularity
	limit    int
}{
	"1D": {market.FiveMinutes, 288},
	"1W": {market.Hour, 168},
	"1M": {market.FourHours, 180},
}

// History returns the curve of pair over a long range, from Binance candles
// (cached for a minute). The demo has no such source: nil.
func (s *session) History(ctx context.Context, pair, rng string) ([]gui.Point, error) {
	hc, ok := historyCandles[rng]
	if s.fixed || !ok {
		return nil, nil
	}
	key := pair + "/" + rng
	s.mu.Lock()
	if h, ok := s.history[key]; ok && time.Since(h.at) < historyCacheFor {
		s.mu.Unlock()
		return h.pts, nil
	}
	s.mu.Unlock()
	bars, err := s.pub.Klines(ctx, pair, hc.interval, hc.limit)
	if err != nil {
		return nil, err
	}
	pts := make([]gui.Point, len(bars))
	for i, b := range bars {
		pts[i] = gui.Point{T: b.Time, V: b.Close}
	}
	s.mu.Lock()
	s.history[key] = historyEntry{at: time.Now(), pts: pts}
	s.mu.Unlock()
	return pts, nil
}

// SetWalletMode switches between the public wallet (read-only, advice) and
// the devnet wallet. Leaving devnet closes the ledger's open positions at
// their last price and stops the bot.
func (s *session) SetWalletMode(mode string) error {
	if mode != gui.ModePublic && mode != gui.ModeDevnet {
		return fmt.Errorf("unknown wallet mode %q", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fixed {
		return errors.New("the wallet modes do not apply to the demo")
	}
	if mode == s.settings.WalletMode {
		return nil
	}
	if mode == gui.ModeDevnet && s.devnetAddr == "" {
		if err := s.loadDevnetKey(); err != nil {
			return err
		}
	}
	prices := s.last.Prices
	costs := live.CostBps(s.eng.Markets(), s.eng.Config())
	s.stopLocked()
	if mode == gui.ModePublic {
		state, err := loop.Load(s.paths.State)
		if err == nil && len(state.Positions) > 0 {
			state, _ = loop.CloseAll(state, prices, loop.Rules{CostBps: costs}, "left devnet", time.Now().UTC())
			err = loop.Save(s.paths.State, state)
		}
		if err != nil {
			_ = s.startLocked()
			return err
		}
	}
	prev := s.settings
	prev.Favorites = slices.Clone(prev.Favorites)
	s.settings.WalletMode = mode
	s.settings.Execute = false // trading is always switched on explicitly
	if err := s.startLocked(); err != nil {
		s.settings = prev
		_ = s.startLocked()
		return err
	}
	s.kickWallet()
	return s.saveSettingsLocked()
}

func (s *session) SetWallet(address string) error {
	address = strings.TrimSpace(address)
	if address != "" {
		if err := wallet.ValidateSolanaAddress(address); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.settings.Wallet = address
	err := s.saveSettingsLocked()
	s.mu.Unlock()
	s.RefreshWallet()
	return err
}

func (s *session) devnetRPCLocked() string {
	if s.settings.DevnetRPC != "" {
		return s.settings.DevnetRPC
	}
	return wallet.DevnetRPC
}

// Airdrop asks the devnet faucet for 1 SOL. The balance is read again a few
// seconds later, once the transaction is confirmed.
func (s *session) Airdrop() error {
	s.mu.Lock()
	addr, rpc, devnet := s.devnetAddr, s.devnetRPCLocked(), s.settings.WalletMode == gui.ModeDevnet
	s.mu.Unlock()
	if !devnet || addr == "" {
		return errors.New("switch the wallet card to devnet first")
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if _, err := wallet.NewSolana(rpc).RequestAirdrop(ctx, addr, wallet.LamportsPerSOL); err != nil {
		return fmt.Errorf("the devnet airdrop was refused (%w).\n\nThe public faucet is rate-limited: you can also request test SOL on %s for the address\n%s",
			err, wallet.DevnetFaucet, addr)
	}
	for _, d := range []time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second} {
		time.AfterFunc(d, s.RefreshWallet)
	}
	return nil
}

// SyncLedger restarts the devnet ledger from the devnet wallet's current
// value (devnet SOL at the real SOL price). In demo, it resets the demo
// ledger to the profile budget.
func (s *session) SyncLedger() error {
	s.mu.Lock()
	if s.fixed {
		defer s.mu.Unlock()
		s.eng.ResetPortfolio()
		return nil
	}
	st := s.settings
	addr := s.devnetAddr
	s.mu.Unlock()
	if st.WalletMode != gui.ModeDevnet || addr == "" {
		return errors.New("switch the wallet card to devnet first")
	}
	view := s.readWallet(gui.WalletView{Devnet: true, Address: addr}, s.devnetRPC(), addr)
	if view.Err != "" {
		return errors.New(view.Err)
	}
	cents := int64(math.Round(view.Total * 100))
	if cents < minLedger {
		return fmt.Errorf("the devnet wallet is worth %s: airdrop at least some devnet SOL first (minimum %s)",
			usdString(cents), usdString(minLedger))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	// A fresh ledger, which keeps the history of the closed trades. Open
	// positions are dropped: the new capital is the wallet's value.
	old, err := loop.Load(s.paths.State)
	if err == nil {
		err = loop.Save(s.paths.State, loop.State{Trades: old.Trades})
	}
	if err != nil {
		_ = s.startLocked()
		return err
	}
	prev := s.settings
	prev.Favorites = slices.Clone(prev.Favorites)
	s.settings.LedgerCapital = cents
	if err := s.startLocked(); err != nil {
		s.settings = prev
		_ = s.startLocked()
		return err
	}
	publish(s.wallets, view)
	return s.saveSettingsLocked()
}

func usdString(cents int64) string { return fmt.Sprintf("$%.2f", float64(cents)/100) }

func (s *session) devnetRPC() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devnetRPCLocked()
}

func (s *session) RefreshWallet() { s.kickWallet() }

func (s *session) kickWallet() {
	select {
	case s.walletKick <- struct{}{}:
	default:
	}
}

func (s *session) SetRPCs(mainnet, devnet string) error {
	mainnet, devnet = strings.TrimSpace(mainnet), strings.TrimSpace(devnet)
	for _, u := range []string{mainnet, devnet} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return errors.New("a Solana RPC must be an http(s) URL")
		}
	}
	s.mu.Lock()
	changed := s.settings.SolanaRPC != mainnet || s.settings.DevnetRPC != devnet
	s.settings.SolanaRPC, s.settings.DevnetRPC = mainnet, devnet
	err := s.saveSettingsLocked()
	s.mu.Unlock()
	if changed {
		s.RefreshWallet()
	}
	return err
}

// SetRiskTolerance updates profile.json (only the risk tolerance: the
// ledger's capital comes from the devnet wallet).
func (s *session) SetRiskTolerance(r int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profile
	p.RiskTolerance = r
	if p.Skills == nil {
		p.Skills = []profile.Skill{}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.RiskTolerance == s.profile.RiskTolerance {
		return nil
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.paths.Profile, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	s.profile = p
	s.eng.SetProfile(s.engineProfileLocked())
	return nil
}

// SetTiming changes the approval window and the max hold (engine restart).
func (s *session) SetTiming(approvalSeconds, maxHoldSeconds int) error {
	if approvalSeconds < gui.MinApprovalSeconds || approvalSeconds > gui.MaxApprovalSeconds ||
		maxHoldSeconds < gui.MinMaxHoldSeconds || maxHoldSeconds > gui.MaxMaxHoldSeconds {
		return errors.New("timing out of bounds")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.settings
	prev.Favorites = slices.Clone(prev.Favorites)
	s.settings.ApprovalSeconds, s.settings.MaxHoldSeconds = approvalSeconds, maxHoldSeconds
	return s.restartLocked(prev)
}

// Quit stops the engine (which saves the ledger) and the wallet loop.
func (s *session) Quit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.stopAll()
}

// walletLoop reads the shown wallet (public or devnet) every walletEvery, or
// when kicked.
func (s *session) walletLoop() {
	var prev gui.WalletView
	s.RefreshWallet()
	ticker := time.NewTicker(walletEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.walletKick:
		}
		s.mu.Lock()
		st, devAddr, rpc := s.settings, s.devnetAddr, s.devnetRPCLocked()
		s.mu.Unlock()
		devnet := st.WalletMode == gui.ModeDevnet && !s.fixed
		addr := st.Wallet
		if devnet {
			addr = devAddr
		} else {
			rpc = st.SolanaRPC
		}
		if addr == "" {
			prev = gui.WalletView{}
			publish(s.wallets, prev)
			continue
		}
		if prev.Address != addr || prev.Devnet != devnet {
			prev = gui.WalletView{Devnet: devnet, Address: addr}
		}
		loading := prev
		loading.Loading = true
		publish(s.wallets, loading)
		prev = s.readWallet(prev, rpc, addr)
		publish(s.wallets, prev)
	}
}

// readWallet reads address on rpc and values it at Binance prices (devnet
// SOL at the real SOL price).
func (s *session) readWallet(prev gui.WalletView, rpc, address string) gui.WalletView {
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
	defer cancel()
	holdings, err := wallet.NewSolana(rpc).Holdings(ctx, address)
	if err != nil {
		prev.Loading, prev.Err = false, "Could not read the wallet: "+err.Error()
		return prev
	}
	v := gui.WalletView{Devnet: prev.Devnet, Address: address, Updated: time.Now()}
	prices := map[string]float64{}
	for _, h := range holdings {
		hv := gui.HoldingView{Holding: h}
		switch {
		case h.USD:
			hv.Price, hv.Priced = 1, true
		case h.Pair != "":
			px, ok := prices[h.Pair]
			if !ok {
				if p, err := s.pub.Price(ctx, h.Pair); err == nil {
					px, ok = p, true
					prices[h.Pair] = p
				}
			} else {
				ok = true
			}
			hv.Price, hv.Priced = px, ok
		}
		if hv.Priced {
			hv.Value = hv.Price * h.Amount
			v.Total += hv.Value
		}
		v.Holdings = append(v.Holdings, hv)
	}
	return v
}
