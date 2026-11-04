package wallet

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// DecodeBase58 decodes s with the Bitcoin/Solana alphabet.
func DecodeBase58(s string) ([]byte, error) {
	n := new(big.Int)
	base := big.NewInt(58)
	for _, c := range s {
		i := strings.IndexRune(base58Alphabet, c)
		if i < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", c)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(i)))
	}
	zeros := len(s) - len(strings.TrimLeft(s, "1"))
	return append(make([]byte, zeros), n.Bytes()...), nil
}

// keypairFromSecret checks a 64-byte Solana secret key (32-byte seed then
// the public key) and returns its key pair.
func keypairFromSecret(b []byte) (Keypair, error) {
	if len(b) != ed25519.PrivateKeySize {
		return Keypair{}, fmt.Errorf("a Solana secret key has %d bytes, not %d", ed25519.PrivateKeySize, len(b))
	}
	priv := ed25519.NewKeyFromSeed(b[:ed25519.SeedSize])
	if !priv.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(b[ed25519.SeedSize:])) {
		return Keypair{}, errors.New("the public half of this secret key does not match its private half")
	}
	return Keypair{Private: priv}, nil
}

// parseKeypairJSON reads the Solana CLI format: a JSON array of the 64
// secret-key bytes.
func parseKeypairJSON(raw []byte) (Keypair, error) {
	var ints []int
	if err := json.Unmarshal(raw, &ints); err != nil {
		return Keypair{}, fmt.Errorf("not a Solana keypair file: %w", err)
	}
	b := make([]byte, 0, len(ints))
	for _, v := range ints {
		if v < 0 || v > 255 {
			return Keypair{}, errors.New("not a Solana keypair file")
		}
		b = append(b, byte(v))
	}
	return keypairFromSecret(b)
}

// ParseSecret reads a wallet secret in any of the formats wallets export:
//   - a 12 to 24-word recovery phrase (Phantom, Solflare, Backpack…),
//     derived on m/44'/501'/account'/0';
//   - a base58 private key (Phantom's "Export private key");
//   - a Solana CLI keypair (a JSON array of 64 numbers).
//
// The error never contains the secret.
func ParseSecret(input string, account uint32) (Keypair, error) {
	s := strings.TrimSpace(input)
	switch {
	case s == "":
		return Keypair{}, errors.New("paste a recovery phrase or a private key")
	case strings.HasPrefix(s, "["):
		return parseKeypairJSON([]byte(s))
	case len(strings.Fields(s)) > 1:
		return KeypairFromMnemonic(s, "", account)
	}
	b, err := DecodeBase58(s)
	if err != nil {
		return Keypair{}, errors.New("not a recovery phrase, a base58 private key nor a keypair file")
	}
	if len(b) == ed25519.PublicKeySize {
		return Keypair{}, errors.New("this is a public address, not a private key: it cannot sign anything")
	}
	return keypairFromSecret(b)
}
