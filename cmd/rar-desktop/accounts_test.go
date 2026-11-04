package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/IlyasFounas/route_after_route/internal/gui"
	"github.com/IlyasFounas/route_after_route/internal/market/binance"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

const abandon = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// fakeCluster is a Solana JSON-RPC endpoint that confirms every transfer.
type fakeCluster struct {
	genesis string
	mu      sync.Mutex
	sent    int
}

func (f *fakeCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Method {
	case "getGenesisHash":
		reply(f.genesis)
	case "getBalance":
		reply(map[string]any{"value": 2 * wallet.LamportsPerSOL})
	case "getLatestBlockhash":
		reply(map[string]any{"value": map[string]any{"blockhash": wallet.EncodeBase58(make([]byte, 32)), "lastValidBlockHeight": 10}})
	case "sendTransaction":
		var b64 string
		json.Unmarshal(req.Params[0], &b64)
		tx, _ := base64.StdEncoding.DecodeString(b64)
		f.sent++
		reply(wallet.EncodeBase58(tx[1:65]))
	case "getSignatureStatuses":
		reply(map[string]any{"value": []any{map[string]any{"confirmationStatus": "confirmed"}}})
	}
}

func newAccountSession(t *testing.T, genesis string) (*session, *fakeCluster) {
	t.Helper()
	dir := t.TempDir()
	cluster := &fakeCluster{genesis: genesis}
	rpc := httptest.NewServer(cluster)
	t.Cleanup(rpc.Close)
	prices := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"symbol":"SOLUSDT","price":"150.00"}`))
	}))
	t.Cleanup(prices.Close)
	pub := binance.NewPublic()
	pub.BaseURL = prices.URL
	st := defaultSettings()
	st.WalletMode, st.DevnetRPC = gui.ModeDevnet, rpc.URL
	s := &session{
		paths:    paths{Dir: dir, Settings: filepath.Join(dir, "settings.json"), DevnetKey: filepath.Join(dir, "devnet-wallet.json"), MainVault: filepath.Join(dir, "main-wallet.json")},
		settings: st, pub: pub, ctx: context.Background(), walletKick: make(chan struct{}, 1),
	}
	if err := s.loadDevnetKey(); err != nil {
		t.Fatal(err)
	}
	return s, cluster
}

func TestImportUnlockForget(t *testing.T) {
	s, _ := newAccountSession(t, wallet.DevnetGenesis)
	if _, err := s.ImportMain(abandon, "short", 0); err == nil {
		t.Fatal("a short password must be refused")
	}
	addr, err := s.ImportMain(abandon, "correct horse", 0)
	if err != nil || addr != "HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk" || s.mainKey == nil {
		t.Fatalf("import: %s, %v", addr, err)
	}
	raw, _ := os.ReadFile(s.paths.MainVault)
	if strings.Contains(string(raw), "abandon") {
		t.Fatal("the phrase is stored in clear")
	}
	settings, _ := os.ReadFile(s.paths.Settings)
	if strings.Contains(string(settings), "abandon") {
		t.Fatal("the phrase leaked into the settings")
	}
	key := s.mainKey.Private
	s.LockMain()
	if s.mainKey != nil || key[0]|key[1]|key[2] != 0 {
		t.Fatal("locking must forget and wipe the key")
	}
	// A new session finds the address without the password.
	s.mainAddr = ""
	if err := s.loadMainLocked(); err != nil || s.mainAddr != addr {
		t.Fatalf("reload: %q %v", s.mainAddr, err)
	}
	if err := s.UnlockMain("wrong password"); !errors.Is(err, wallet.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := s.UnlockMain("correct horse"); err != nil || s.mainKey == nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := s.ForgetMain(); err != nil || s.mainAddr != "" || s.mainKey != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := os.Stat(s.paths.MainVault); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the vault must be deleted")
	}
	// The bot's own key cannot be the main account.
	botSecret, _ := os.ReadFile(s.paths.DevnetKey)
	if _, err := s.ImportMain(string(botSecret), "correct horse", 0); err == nil {
		t.Fatal("the bot account cannot be imported as the main account")
	}
}

func TestFundNeedsUnlockedMain(t *testing.T) {
	s, cluster := newAccountSession(t, wallet.DevnetGenesis)
	if _, err := s.FundBot(1e8); err == nil {
		t.Fatal("no main account: nothing to fund from")
	}
	s.ImportMain(abandon, "correct horse", 0)
	s.LockMain()
	if _, err := s.FundBot(1e8); err == nil || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("locked main account: %v", err)
	}
	if cluster.sent != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestWithdrawToLockedMain(t *testing.T) {
	s, cluster := newAccountSession(t, wallet.DevnetGenesis)
	s.ImportMain(abandon, "correct horse", 0)
	s.LockMain() // withdrawing only needs the main address
	sig, err := s.WithdrawToMain(5e7)
	if err != nil || sig == "" || cluster.sent != 1 {
		t.Fatalf("withdraw: %q %v (sent %d)", sig, err, cluster.sent)
	}
}

func TestTransfersRefuseMainnet(t *testing.T) {
	s, cluster := newAccountSession(t, wallet.MainnetGenesis)
	s.ImportMain(abandon, "correct horse", 0)
	if _, err := s.FundBot(1e8); !errors.Is(err, wallet.ErrNotTestCluster) {
		t.Fatalf("fund on mainnet: %v", err)
	}
	if _, err := s.WithdrawToMain(1e8); !errors.Is(err, wallet.ErrNotTestCluster) {
		t.Fatalf("withdraw on mainnet: %v", err)
	}
	if cluster.sent != 0 {
		t.Fatal("nothing may be sent to mainnet")
	}
}

func TestSetMaxTrade(t *testing.T) {
	s, _ := newAccountSession(t, wallet.DevnetGenesis)
	if s.settings.MaxTradeCents != defaultMaxTrade {
		t.Fatalf("default %d", s.settings.MaxTradeCents)
	}
	s.settings.LedgerCapital = 5000
	for _, bad := range []int64{0, 99, 5001} {
		if err := s.SetMaxTrade(bad); err == nil {
			t.Errorf("%d accepted", bad)
		}
	}
	if err := s.SetMaxTrade(2500); err != nil || s.settings.MaxTradeCents != 2500 {
		t.Fatalf("%v %d", err, s.settings.MaxTradeCents)
	}
	if st, _ := loadSettings(s.paths.Settings); st.MaxTradeCents != 2500 {
		t.Fatalf("saved %d", st.MaxTradeCents)
	}
}
