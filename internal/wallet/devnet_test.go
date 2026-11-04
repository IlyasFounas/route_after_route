package wallet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBase58RoundTrip(t *testing.T) {
	for _, addr := range []string{"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", "11111111111111111111111111111111", tokenProgram} {
		n, err := decodeForTest(addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := EncodeBase58(n); got != addr {
			t.Fatalf("EncodeBase58 = %s, want %s", got, addr)
		}
	}
}

// decodeForTest is a minimal base58 decoder (32-byte keys).
func decodeForTest(s string) ([]byte, error) {
	out := make([]byte, 32)
	for _, c := range s {
		carry := strings.IndexRune(base58Alphabet, c)
		for i := len(out) - 1; i >= 0; i-- {
			carry += 58 * int(out[i])
			out[i] = byte(carry)
			carry >>= 8
		}
	}
	return out, nil
}

func TestKeypairFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devnet-wallet.json")
	k1, err := LoadOrCreateKeypair(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSolanaAddress(k1.Address()); err != nil {
		t.Fatalf("generated address %q: %v", k1.Address(), err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("keypair mode = %v, want 0600", st.Mode().Perm())
	}
	var ints []int
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &ints); err != nil || len(ints) != 64 {
		t.Fatalf("not the Solana CLI format: %s", raw)
	}
	k2, err := LoadOrCreateKeypair(path)
	if err != nil || k2.Address() != k1.Address() {
		t.Fatalf("reload: %v, %s != %s", err, k2.Address(), k1.Address())
	}
	ints[40] ^= 1 // corrupt the public half
	raw, _ = json.Marshal(ints)
	os.WriteFile(path, raw, 0o600)
	if _, err := LoadOrCreateKeypair(path); err == nil {
		t.Fatal("a corrupted keypair must be rejected")
	}
}

func TestRequestAirdrop(t *testing.T) {
	addr := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		json.Unmarshal(body, &req)
		if req.Method != "requestAirdrop" || req.Params[0] != addr || req.Params[1] != float64(LamportsPerSOL) {
			t.Errorf("unexpected request %s", body)
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"5sig"}`))
	}))
	defer srv.Close()
	sig, err := (&Solana{RPC: srv.URL, HTTP: srv.Client()}).RequestAirdrop(context.Background(), addr, LamportsPerSOL)
	if err != nil || sig != "5sig" {
		t.Fatalf("sig %q, err %v", sig, err)
	}
	if _, err := NewSolana("").RequestAirdrop(context.Background(), addr, 1); err == nil {
		t.Fatal("mainnet airdrop must be refused")
	}
}
