package wallet

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

// bip39English is the official BIP-39 English word list (2048 words,
// sha256 2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda).
//
//go:embed bip39_english.txt
var bip39English string

var (
	wordsOnce sync.Once
	words     []string
	wordIndex map[string]int
)

func bip39Words() ([]string, map[string]int) {
	wordsOnce.Do(func() {
		words = strings.Fields(bip39English)
		wordIndex = make(map[string]int, len(words))
		for i, w := range words {
			wordIndex[w] = i
		}
	})
	return words, wordIndex
}

// normalizeMnemonic lowercases the phrase and collapses its spaces.
func normalizeMnemonic(m string) []string {
	return strings.Fields(strings.ToLower(m))
}

// ValidateMnemonic checks a BIP-39 phrase: 12 to 24 words of the English
// list, and its checksum.
func ValidateMnemonic(mnemonic string) error {
	ws := normalizeMnemonic(mnemonic)
	switch len(ws) {
	case 12, 15, 18, 21, 24:
	default:
		return fmt.Errorf("a recovery phrase has 12, 15, 18, 21 or 24 words, not %d", len(ws))
	}
	_, index := bip39Words()
	n := new(big.Int)
	for i, w := range ws {
		v, ok := index[w]
		if !ok {
			return fmt.Errorf("word %d (%q) is not in the BIP-39 English list", i+1, w)
		}
		n.Lsh(n, 11)
		n.Or(n, big.NewInt(int64(v)))
	}
	total := len(ws) * 11
	csBits := total / 33
	entBits := total - csBits
	checksum := new(big.Int).And(n, big.NewInt(int64(1)<<csBits-1))
	entropy := new(big.Int).Rsh(n, uint(csBits)).FillBytes(make([]byte, entBits/8))
	sum := sha256.Sum256(entropy)
	if want := uint64(sum[0]) >> (8 - csBits); checksum.Uint64() != want {
		return errors.New("the recovery phrase checksum is wrong: check the words and their order")
	}
	return nil
}

// MnemonicSeed is the BIP-39 seed of a valid phrase (PBKDF2-HMAC-SHA512,
// 2048 rounds, salt "mnemonic"+passphrase). The passphrase must be ASCII:
// the NFKD normalisation of other characters is not implemented.
func MnemonicSeed(mnemonic, passphrase string) ([]byte, error) {
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, err
	}
	for _, r := range passphrase {
		if r > 127 {
			return nil, errors.New("only ASCII passphrases are supported")
		}
	}
	phrase := strings.Join(normalizeMnemonic(mnemonic), " ")
	return pbkdf2.Key(sha512.New, phrase, []byte("mnemonic"+passphrase), 2048, 64)
}

// Hardened marks a hardened derivation index.
const Hardened = 0x80000000

// SolanaPath is the derivation path of Phantom, Solflare and Backpack:
// m/44'/501'/account'/0'.
func SolanaPath(account uint32) []uint32 {
	return []uint32{44 | Hardened, 501 | Hardened, account | Hardened, 0 | Hardened}
}

// DeriveEd25519 derives an ed25519 private seed from a BIP-39 seed along
// path (SLIP-0010; ed25519 only has hardened children).
func DeriveEd25519(seed []byte, path []uint32) ([]byte, error) {
	mac := hmac.New(sha512.New, []byte("ed25519 seed"))
	mac.Write(seed)
	i := mac.Sum(nil)
	key, chain := i[:32], i[32:]
	for _, idx := range path {
		if idx < Hardened {
			return nil, errors.New("ed25519 derivation only allows hardened indexes")
		}
		data := make([]byte, 0, 37)
		data = append(data, 0)
		data = append(data, key...)
		data = binary.BigEndian.AppendUint32(data, idx)
		mac := hmac.New(sha512.New, chain)
		mac.Write(data)
		i = mac.Sum(nil)
		key, chain = i[:32], i[32:]
	}
	return key, nil
}

// KeypairFromMnemonic derives the wallet of a recovery phrase on the
// Phantom/Solflare path, for the given account number (0 = first account).
func KeypairFromMnemonic(mnemonic, passphrase string, account uint32) (Keypair, error) {
	seed, err := MnemonicSeed(mnemonic, passphrase)
	if err != nil {
		return Keypair{}, err
	}
	k, err := DeriveEd25519(seed, SolanaPath(account))
	if err != nil {
		return Keypair{}, err
	}
	return Keypair{Private: ed25519.NewKeyFromSeed(k)}, nil
}
