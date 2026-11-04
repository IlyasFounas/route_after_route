package wallet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A syntactically valid address (the system program id is 32 zero bytes).
const addr = "11111111111111111111111111111111"

func TestValidateSolanaAddress(t *testing.T) {
	for _, ok := range []string{addr, "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", " TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA "} {
		if err := ValidateSolanaAddress(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "hello", "0x52908400098527886E0F7030069857D2E4169EE7", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1O", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1vEPj"} {
		if err := ValidateSolanaAddress(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestHoldings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "getBalance":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":8200000000}}`))
		case "getTokenAccountsByOwner":
			var prog struct {
				ProgramID string `json:"programId"`
			}
			json.Unmarshal(req.Params[1], &prog)
			if prog.ProgramID != tokenProgram {
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":[]}}`))
				return
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"value":[
				{"account":{"data":{"parsed":{"info":{"mint":"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v","tokenAmount":{"uiAmountString":"100"}}}}}},
				{"account":{"data":{"parsed":{"info":{"mint":"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v","tokenAmount":{"uiAmountString":"26.5"}}}}}},
				{"account":{"data":{"parsed":{"info":{"mint":"Unknown1111111111111111111111111111111Mint","tokenAmount":{"uiAmountString":"3"}}}}}},
				{"account":{"data":{"parsed":{"info":{"mint":"DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263","tokenAmount":{"uiAmountString":"0"}}}}}}
			]}}`))
		}
	}))
	defer srv.Close()
	hs, err := (&Solana{RPC: srv.URL, HTTP: srv.Client()}).Holdings(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 3 {
		t.Fatalf("got %+v", hs)
	}
	if hs[0].Symbol != "SOL" || hs[0].Amount != 8.2 || hs[0].Pair != "SOLUSDT" {
		t.Fatalf("SOL = %+v", hs[0])
	}
	if hs[1].Symbol != "USDC" || hs[1].Amount != 126.5 {
		t.Fatalf("USDC accounts should be summed: %+v", hs[1])
	}
	if hs[2].Known() || hs[2].Pair != "" {
		t.Fatalf("unknown token should not be valued: %+v", hs[2])
	}
}

func TestRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"rate limited"}}`))
	}))
	defer srv.Close()
	if _, err := (&Solana{RPC: srv.URL, HTTP: srv.Client()}).Holdings(context.Background(), addr); err == nil {
		t.Fatal("expected the RPC error")
	}
}
