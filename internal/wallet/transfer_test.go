package wallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransferMessageLayout(t *testing.T) {
	from := make([]byte, 32)
	to := make([]byte, 32)
	hash := make([]byte, 32)
	for i := range 32 {
		from[i], to[i], hash[i] = 1, 2, 3
	}
	got := hex.EncodeToString(transferMessage(from, to, hash, 1_500_000_000))
	want := "010001" + "03" + strings.Repeat("01", 32) + strings.Repeat("02", 32) + strings.Repeat("00", 32) +
		strings.Repeat("03", 32) + "01" + "02" + "020001" + "0c" + "02000000" + "002f685900000000"
	if got != want {
		t.Fatalf("message\n got %s\nwant %s", got, want)
	}
}

// fakeRPC is a JSON-RPC cluster that records sendTransaction.
type fakeRPC struct {
	genesis string
	mu      sync.Mutex
	sent    []byte
	polls   int
}

func (f *fakeRPC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
	switch req.Method {
	case "getGenesisHash":
		reply(f.genesis)
	case "getLatestBlockhash":
		reply(map[string]any{"value": map[string]any{"blockhash": EncodeBase58(make([]byte, 32)), "lastValidBlockHeight": 100}})
	case "getFeeForMessage":
		reply(map[string]any{"value": 5000})
	case "sendTransaction":
		var b64 string
		json.Unmarshal(req.Params[0], &b64)
		f.sent, _ = base64.StdEncoding.DecodeString(b64)
		reply(EncodeBase58(f.sent[1:65]))
	case "getSignatureStatuses":
		f.polls++
		if f.polls < 2 {
			reply(map[string]any{"value": []any{nil}})
			return
		}
		reply(map[string]any{"value": []any{map[string]any{"confirmationStatus": "confirmed", "err": nil}}})
	case "getBlockHeight":
		reply(50)
	}
}

func TestTransferSignsAndConfirms(t *testing.T) {
	confirmEvery = time.Millisecond
	defer func() { confirmEvery = time.Second }()
	rpc := &fakeRPC{genesis: DevnetGenesis}
	srv := httptest.NewServer(rpc)
	defer srv.Close()
	s := &Solana{RPC: srv.URL, HTTP: srv.Client()}
	from, _ := ParseSecret(abandon, 0)
	to, _ := ParseSecret(abandon, 1)
	ctx := context.Background()
	if fee, err := s.TransferFee(ctx, from, to.Address(), 1000); err != nil || fee != 5000 {
		t.Fatalf("fee %d, %v", fee, err)
	}
	sig, err := s.Transfer(ctx, from, to.Address(), 250_000_000)
	if err != nil {
		t.Fatal(err)
	}
	tx := rpc.sent
	if tx[0] != 1 || len(tx) != 1+64+len(transferMessage(make([]byte, 32), make([]byte, 32), make([]byte, 32), 0)) {
		t.Fatalf("transaction of %d bytes", len(tx))
	}
	msg := tx[65:]
	if !ed25519.Verify(from.Private.Public().(ed25519.PublicKey), msg, tx[1:65]) {
		t.Fatal("bad signature")
	}
	if sig != EncodeBase58(tx[1:65]) || rpc.polls != 2 {
		t.Fatalf("sig %s, polls %d", sig, rpc.polls)
	}
	if _, err := s.Transfer(ctx, from, from.Address(), 1); err == nil {
		t.Fatal("a transfer to itself must be refused")
	}
}

func TestTransferRefusesMainnet(t *testing.T) {
	for _, g := range []string{MainnetGenesis, "SomeLocalValidatorGenesis1111111111111111"} {
		rpc := &fakeRPC{genesis: g}
		srv := httptest.NewServer(rpc)
		s := &Solana{RPC: srv.URL, HTTP: srv.Client()}
		from, _ := ParseSecret(abandon, 0)
		to, _ := ParseSecret(abandon, 1)
		if _, err := s.Transfer(context.Background(), from, to.Address(), 1); !errors.Is(err, ErrNotTestCluster) {
			t.Errorf("genesis %s: %v", g, err)
		}
		if rpc.sent != nil {
			t.Errorf("genesis %s: a transaction was sent", g)
		}
		srv.Close()
	}
	s := NewSolana("https://my-mainnet-node.example")
	from, _ := ParseSecret(abandon, 0)
	if _, err := s.Transfer(context.Background(), from, addr, 1); !errors.Is(err, ErrNotTestCluster) {
		t.Errorf("a mainnet URL must be refused before any request: %v", err)
	}
}
