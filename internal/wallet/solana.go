// Package wallet reads crypto wallets. Mainnet wallets are strictly
// read-only: only PUBLIC addresses are handled. The only private key this
// package ever touches is the app-generated DEVNET test key (devnet.go),
// which holds worthless test SOL.
package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultSolanaRPC is the public Solana mainnet endpoint (rate-limited).
const DefaultSolanaRPC = "https://api.mainnet-beta.solana.com"

const (
	tokenProgram     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	token2022Program = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

// Holding is the balance of one asset.
type Holding struct {
	Symbol string  `json:"symbol"` // "SOL", "USDC"… or a shortened mint when unknown
	Name   string  `json:"name"`
	Mint   string  `json:"mint,omitempty"` // empty for native SOL
	Amount float64 `json:"amount"`
	// Pair is the Binance USDT pair used to value the asset ("" when none).
	Pair string `json:"pair,omitempty"`
	// USD is true for assets worth one US dollar (USDT itself).
	USD bool `json:"usd,omitempty"`
}

// Known reports whether the asset is a recognised token.
func (h Holding) Known() bool { return h.Name != "" }

type tokenInfo struct {
	symbol, name, pair string
	usd                bool
}

// knownTokens maps mainnet mints to their symbol and Binance pair. Liquid
// staking tokens have no pair: valuing them at the SOL price would be wrong.
var knownTokens = map[string]tokenInfo{
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": {"USDC", "USD Coin", "USDCUSDT", false},
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": {"USDT", "Tether USD", "", true},
	"HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr": {"EURC", "Euro Coin", "EURUSDT", false},
	"JUPyiwrYJFskUPiHa7hkeR8VUtAeFoSYbKedZNsDvCN":  {"JUP", "Jupiter", "JUPUSDT", false},
	"DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263": {"BONK", "Bonk", "BONKUSDT", false},
	"EKpQGSJtjMFqKZ9KQanSqYXRcF8fBopzLHYxdM65zcjm": {"WIF", "dogwifhat", "WIFUSDT", false},
	"HZ1JovNiVvGrGNiiYvEozEVgZ58xaU3RKwX8eACQBCt3": {"PYTH", "Pyth Network", "PYTHUSDT", false},
	"jtojtomepa8beP8AuQc6eXt5FriJwfFMwQx2v2f9mCL":  {"JTO", "Jito", "JTOUSDT", false},
	"4k3Dyjzvzp8eMZWUXbBCjEvwSkkk59S5iCNLY3QrkX6R": {"RAY", "Raydium", "RAYUSDT", false},
	"mSoLzYCxHdYgdzU16g5QSh3i5K3z3KZK7ytfqcJm7So":  {"mSOL", "Marinade staked SOL", "", false},
	"J1toso1uCk3RLmjorhTtrVwY9HJ7X8V9yYac6Y7kGCPn": {"JitoSOL", "Jito staked SOL", "", false},
}

// Solana reads balances through the Solana JSON-RPC API.
type Solana struct {
	RPC  string
	HTTP *http.Client
}

// NewSolana returns a reader on rpc (DefaultSolanaRPC when empty).
func NewSolana(rpc string) *Solana {
	if rpc == "" {
		rpc = DefaultSolanaRPC
	}
	return &Solana{RPC: rpc, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ValidateSolanaAddress checks that s is a base58-encoded 32-byte public key.
func ValidateSolanaAddress(s string) error {
	s = strings.TrimSpace(s)
	if len(s) < 32 || len(s) > 44 {
		return errors.New("a Solana address has 32 to 44 characters")
	}
	n := new(big.Int)
	for _, c := range s {
		i := strings.IndexRune(base58Alphabet, c)
		if i < 0 {
			return fmt.Errorf("invalid character %q in a Solana address", c)
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(i)))
	}
	leadingZeros := len(s) - len(strings.TrimLeft(s, "1"))
	if size := leadingZeros + len(n.Bytes()); size != 32 {
		return fmt.Errorf("not a Solana public key (%d bytes instead of 32)", size)
	}
	return nil
}

// Holdings returns the non-zero balances of address: native SOL first, then
// tokens sorted by symbol.
func (s *Solana) Holdings(ctx context.Context, address string) ([]Holding, error) {
	address = strings.TrimSpace(address)
	if err := ValidateSolanaAddress(address); err != nil {
		return nil, err
	}
	var bal struct {
		Value uint64 `json:"value"`
	}
	if err := s.call(ctx, "getBalance", []any{address, map[string]string{"commitment": "confirmed"}}, &bal); err != nil {
		return nil, err
	}
	var out []Holding
	if bal.Value > 0 {
		out = append(out, Holding{Symbol: "SOL", Name: "Solana", Amount: float64(bal.Value) / 1e9, Pair: "SOLUSDT"})
	}
	amounts := map[string]float64{}
	for _, program := range []string{tokenProgram, token2022Program} {
		var res struct {
			Value []struct {
				Account struct {
					Data struct {
						Parsed struct {
							Info struct {
								Mint        string `json:"mint"`
								TokenAmount struct {
									UIAmountString string `json:"uiAmountString"`
								} `json:"tokenAmount"`
							} `json:"info"`
						} `json:"parsed"`
					} `json:"data"`
				} `json:"account"`
			} `json:"value"`
		}
		params := []any{address, map[string]string{"programId": program}, map[string]string{"encoding": "jsonParsed", "commitment": "confirmed"}}
		if err := s.call(ctx, "getTokenAccountsByOwner", params, &res); err != nil {
			return nil, err
		}
		for _, acc := range res.Value {
			info := acc.Account.Data.Parsed.Info
			amt, err := strconv.ParseFloat(info.TokenAmount.UIAmountString, 64)
			if err != nil || amt <= 0 {
				continue
			}
			amounts[info.Mint] += amt
		}
	}
	var tokens []Holding
	for mint, amt := range amounts {
		h := Holding{Mint: mint, Amount: amt, Symbol: mint[:4] + "…" + mint[len(mint)-4:]}
		if t, ok := knownTokens[mint]; ok {
			h.Symbol, h.Name, h.Pair, h.USD = t.symbol, t.name, t.pair, t.usd
		}
		tokens = append(tokens, h)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].Known() != tokens[j].Known() {
			return tokens[i].Known()
		}
		return tokens[i].Symbol < tokens[j].Symbol
	})
	return append(out, tokens...), nil
}

func (s *Solana) call(ctx context.Context, method string, params any, result any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.RPC, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("solana %s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("solana %s: %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("solana %s: %s (code %d)", method, env.Error.Message, env.Error.Code)
	}
	return json.Unmarshal(env.Result, result)
}
