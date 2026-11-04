package wallet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// DevnetRPC is the public Solana devnet endpoint. Devnet SOL has no value.
const DevnetRPC = "https://api.devnet.solana.com"

// DevnetFaucet is the web faucet to use when the RPC airdrop is rate-limited.
const DevnetFaucet = "https://faucet.solana.com"

// LamportsPerSOL is the number of lamports in one SOL.
const LamportsPerSOL = 1_000_000_000

// Keypair is a Solana ed25519 key pair.
type Keypair struct {
	Private ed25519.PrivateKey
}

// Address is the base58 public key.
func (k Keypair) Address() string {
	return EncodeBase58(k.Private.Public().(ed25519.PublicKey))
}

// LoadOrCreateKeypair reads the key pair at path, in the Solana CLI format (a
// JSON array of the 64 secret-key bytes), or creates one there with mode
// 0600. It is meant for the devnet test wallet only.
func LoadOrCreateKeypair(path string) (Keypair, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return createKeypair(path)
	}
	if err != nil {
		return Keypair{}, err
	}
	kp, err := parseKeypairJSON(raw)
	if err != nil {
		return Keypair{}, fmt.Errorf("%s: %w", path, err)
	}
	return kp, nil
}

func createKeypair(path string) (Keypair, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Keypair{}, err
	}
	ints := make([]int, len(priv))
	for i, v := range priv {
		ints[i] = int(v)
	}
	raw, _ := json.Marshal(ints)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Keypair{}, err
	}
	// O_EXCL: never overwrite a key created concurrently.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Keypair{}, err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return Keypair{}, err
	}
	if err := f.Close(); err != nil {
		return Keypair{}, err
	}
	return Keypair{Private: priv}, nil
}

// EncodeBase58 encodes b with the Bitcoin/Solana alphabet.
func EncodeBase58(b []byte) string {
	n := new(big.Int).SetBytes(b)
	var out []byte
	mod, base := new(big.Int), big.NewInt(58)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, v := range b {
		if v != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// RequestAirdrop asks the RPC (devnet or testnet only) to send lamports to
// address, and returns the transaction signature. Public endpoints are
// heavily rate-limited: on failure, point the user to DevnetFaucet.
func (s *Solana) RequestAirdrop(ctx context.Context, address string, lamports uint64) (string, error) {
	address = strings.TrimSpace(address)
	if err := ValidateSolanaAddress(address); err != nil {
		return "", err
	}
	if strings.Contains(s.RPC, "mainnet") {
		return "", errors.New("airdrops only exist on devnet and testnet")
	}
	var sig string
	if err := s.call(ctx, "requestAirdrop", []any{address, lamports, map[string]string{"commitment": "confirmed"}}, &sig); err != nil {
		return "", err
	}
	return sig, nil
}
