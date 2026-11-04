package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rar "github.com/IlyasFounas/route_after_route/internal/app"
	"github.com/IlyasFounas/route_after_route/internal/gui"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	_ "github.com/IlyasFounas/route_after_route/internal/market/binance"
)

type stubMarket struct{ market.Market }

func (stubMarket) Descriptor() market.Descriptor { return market.Descriptor{ID: "binance-spot"} }

func TestSettleForeign(t *testing.T) {
	st := loop.State{Cycle: 3, Cash: 100, Positions: []loop.Position{
		{Market: "binance-spot", Symbol: "SOLUSDT", Direction: 1, Amount: 500, EntryPrice: 10},
		{Market: "demo-range", Symbol: "RANGE", Direction: 1, Amount: 700, EntryPrice: 99},
	}}
	got := settleForeign(st, []market.Market{stubMarket{}}, nil, nil)
	if len(got.Positions) != 1 || got.Positions[0].Market != "binance-spot" || got.Cash != 800 {
		t.Fatalf("%+v", got)
	}
	// With a last price, the position is closed at that price.
	got = settleForeign(st, []market.Market{stubMarket{}}, loop.Prices{{Market: "demo-range", Symbol: "RANGE"}: 99 * 1.1}, nil)
	if got.Cash != 100+770 {
		t.Fatalf("cash %d, want 870", got.Cash)
	}
}

func TestBootstrapAndMigration(t *testing.T) {
	dir := t.TempDir()
	// An old install: the replay demo as config.json, with its state.
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"markets":[{"type":"csvfile","options":{"replay":{}}}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"cycle":5}`), 0o644)
	if err := bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if !strings.Contains(string(raw), `"binance"`) {
		t.Fatalf("config.json should now be the live template: %s", raw)
	}
	for _, f := range []string{"config.replay-old.json", "state.replay-old.json", "profile.json", "demo/config.json", "demo/testdata/range/RANGE.csv"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
		t.Error("the old replay state should have been moved aside")
	}
	demo, _ := os.ReadFile(filepath.Join(dir, "demo/config.json"))
	if !strings.Contains(string(demo), `"testdata/trend"`) {
		t.Fatalf("demo paths not rewritten: %s", demo)
	}
	// A second run changes nothing.
	if err := bootstrap(dir); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSettingsSanitizes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	// An older version: single pair, 15m interval, no favorites.
	raw, _ := json.Marshal(map[string]any{"pair": "RANGE", "interval": "15m", "range": "2Y", "wallet": "not-an-address", "execute": true,
		"favorites": []string{"ethusdt", "ETHUSDT", "BAD", "BTCUSDT"}, "wallet_mode": "mainnet", "approval_seconds": 999})
	os.WriteFile(p, raw, 0o644)
	st, err := loadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pair != "ETHUSDT" || st.Wallet != "" || !st.Execute || st.Range != gui.DefaultRange || st.WalletMode != gui.ModePublic {
		t.Fatalf("%+v", st)
	}
	if !slices.Equal(st.Favorites, []string{"ETHUSDT", "BTCUSDT"}) || st.ApprovalSeconds != defaultApproval || st.MaxHoldSeconds != defaultMaxHold {
		t.Fatalf("%+v", st)
	}
	// The shown pair is always a favorite.
	raw, _ = json.Marshal(map[string]any{"pair": "SOLUSDT"})
	os.WriteFile(p, raw, 0o644)
	if st, _ = loadSettings(p); !slices.Equal(st.Favorites, []string{"SOLUSDT"}) {
		t.Fatalf("%+v", st.Favorites)
	}
}

func TestEngineConfigWatchesEachFavorite(t *testing.T) {
	cfg := rar.DefaultConfig()
	s := &session{cfg: cfg, settings: gui.Settings{Pair: "SOLUSDT", Favorites: []string{"SOLUSDT", "BTCUSDT", "ETHUSDT"}}}
	got := s.engineConfig()
	if len(got.Markets) != 3 || got.Period.Lookback.Duration != liveLookback || got.ReevaluateEvery.Duration != liveReevaluate {
		t.Fatalf("%+v", got)
	}
	ms, err := got.OpenMarkets()
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range ms {
		d := m.Descriptor()
		if d.ID != marketID(s.settings.Favorites[i]) || d.Granularity != market.Second || len(d.Symbols) != 1 {
			t.Errorf("market %d: %+v", i, d)
		}
	}
}

func TestCanTrade(t *testing.T) {
	s := &session{settings: gui.Settings{WalletMode: gui.ModePublic, LedgerCapital: 1_000_000}}
	if s.canTradeLocked() == nil {
		t.Fatal("the public wallet must never trade")
	}
	s.settings.WalletMode = gui.ModeDevnet
	s.settings.LedgerCapital = 0
	if s.canTradeLocked() == nil {
		t.Fatal("an unfunded devnet ledger must not trade")
	}
	s.settings.LedgerCapital = 50_000
	if err := s.canTradeLocked(); err != nil {
		t.Fatal(err)
	}
	s.profile.Capital = 1_000_000
	if p := s.engineProfileLocked(); p.Capital != 50_000 {
		t.Fatalf("devnet ledger capital = %d", p.Capital)
	}
}
