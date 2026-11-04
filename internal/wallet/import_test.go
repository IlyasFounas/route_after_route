package wallet

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const abandon = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// The official BIP-39 vectors (passphrase "TREZOR").
func TestMnemonicSeedVectors(t *testing.T) {
	for m, want := range map[string]string{
		abandon: "c55257c360c07c72029aebc1b53c05ed0362ada38ead3e3e9efa3708e53495531f09a6987599d18264c1e1c92f2cf141630c7a3c4ab7c81b2f001698e7463b04",
		"legal winner thank year wave sausage worth useful legal winner thank yellow": "2e8905819b8723fe2c1d161860e5ee1830318dbf49a83bd451cfb8440c28bd6fa457fe1296106559a3c80937a1c1069be3a3a5bd381ee6260e8d9739fce1f607",
		"zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong":                           "ac27495480225222079d7be181583751e86f571027b0497b5b5d11218e0a8a13332572917f0f8e5a589620c6f15b11c61dee327651a14c34e18231052e48c069",
	} {
		seed, err := MnemonicSeed(m, "TREZOR")
		if err != nil {
			t.Fatalf("%q: %v", m, err)
		}
		if got := hex.EncodeToString(seed); got != want {
			t.Errorf("%q: seed %s, want %s", m, got, want)
		}
	}
}

func TestValidateMnemonic(t *testing.T) {
	if err := ValidateMnemonic("  ABANDON abandon abandon abandon abandon abandon\nabandon abandon abandon abandon abandon about "); err != nil {
		t.Fatalf("case and spaces must not matter: %v", err)
	}
	for _, bad := range []string{
		strings.Repeat("abandon ", 12),                 // wrong checksum
		strings.Repeat("abandon ", 11),                 // 11 words
		strings.Replace(abandon, "about", "aboutt", 1), // not a word
		"about abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon", // order
	} {
		if err := ValidateMnemonic(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// SLIP-0010 test vector 1 for ed25519.
func TestDeriveEd25519Vectors(t *testing.T) {
	seed, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	for _, c := range []struct {
		path      []uint32
		priv, pub string
	}{
		{nil, "2b4be7f19ee27bbf30c667b642d5f4aa69fd169872f8fc3059c08ebae2eb19e7", "a4b2856bfec510abab89753fac1ac0e1112364e7d250545963f135f2a33188ed"},
		{[]uint32{Hardened}, "68e0fe46dfb67e368c75379acec591dad19df3cde26e63b93a8e704f1dade7a3", "8c8a13df77a28f3445213a0f432fde644acaa215fc72dcdf300d5efaa85d350c"},
		{[]uint32{Hardened, Hardened + 1, Hardened + 2}, "92a5b23c0b8a99e37d07df3fb9966917f5d06e02ddbd909c7e184371463e9fc9", "ae98736566d30ed0e9d2f4486a64bc95740d89c7db33f52121f8ea8f76ff0fc1"},
	} {
		k, err := DeriveEd25519(seed, c.path)
		if err != nil {
			t.Fatal(err)
		}
		pub := ed25519.NewKeyFromSeed(k).Public().(ed25519.PublicKey)
		if hex.EncodeToString(k) != c.priv || hex.EncodeToString(pub) != c.pub {
			t.Errorf("%v: %x / %x", c.path, k, pub)
		}
	}
	if _, err := DeriveEd25519(seed, []uint32{1}); err == nil {
		t.Error("a non-hardened index must be refused")
	}
}

// The abandon…about phrase gives this address in Phantom and Solflare.
func TestPhantomAddress(t *testing.T) {
	for account, want := range map[uint32]string{0: "HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk", 1: "Hh8QwFUA6MtVu1qAoq12ucvFHNwCcVTV7hpWjeY1Hztb"} {
		kp, err := ParseSecret(abandon, account)
		if err != nil || kp.Address() != want {
			t.Errorf("account %d: %s, %v; want %s", account, kp.Address(), err, want)
		}
	}
}

func TestParseSecretFormats(t *testing.T) {
	want, _ := ParseSecret(abandon, 0)
	b58 := EncodeBase58(want.Private)
	ints := make([]int, len(want.Private))
	for i, v := range want.Private {
		ints[i] = int(v)
	}
	cli, _ := json.Marshal(ints)
	for name, in := range map[string]string{"base58": "  " + b58 + "\n", "cli": string(cli)} {
		kp, err := ParseSecret(in, 7) // the account only applies to phrases
		if err != nil || kp.Address() != want.Address() {
			t.Errorf("%s: %s, %v", name, kp.Address(), err)
		}
	}
	tampered := append(ed25519.PrivateKey{}, want.Private...)
	tampered[40] ^= 1
	for name, in := range map[string]string{
		"empty":   "",
		"address": want.Address(),
		"garbage": "not0OIl",
		"short":   EncodeBase58(want.Private[:40]),
		"tamper":  EncodeBase58(tampered),
		"json":    "[1,2,3]",
	} {
		_, err := ParseSecret(in, 0)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		if strings.Contains(err.Error(), b58[:10]) {
			t.Errorf("%s: the error leaks the secret: %v", name, err)
		}
	}
}

func TestDecodeBase58RoundTrip(t *testing.T) {
	for _, b := range [][]byte{{}, {0}, {0, 0, 1}, {255, 254}, make([]byte, 32)} {
		got, err := DecodeBase58(EncodeBase58(b))
		if err != nil || hex.EncodeToString(got) != hex.EncodeToString(b) {
			t.Errorf("%x → %x, %v", b, got, err)
		}
	}
}

func TestVault(t *testing.T) {
	vaultIterations = 1000
	defer func() { vaultIterations = 600_000 }()
	kp, _ := ParseSecret(abandon, 0)
	path := filepath.Join(t.TempDir(), "main-wallet.json")
	if err := SaveVault(path, kp, "short"); err == nil {
		t.Fatal("a short password must be refused")
	}
	if err := SaveVault(path, kp, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode %v", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), EncodeBase58(kp.Private)) || strings.Contains(string(raw), "abandon") {
		t.Fatal("the vault holds the secret in clear")
	}
	if a, err := VaultAddress(path); err != nil || a != kp.Address() {
		t.Fatalf("address %q, %v", a, err)
	}
	got, err := OpenVault(path, "correct horse")
	if err != nil || got.Address() != kp.Address() || !got.Private.Equal(kp.Private) {
		t.Fatalf("open: %v", err)
	}
	if _, err := OpenVault(path, "correct horsE"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	// The clear address is bound to the ciphertext: it cannot be swapped.
	var v vaultFile
	json.Unmarshal(raw, &v)
	v.Address = "Hh8QwFUA6MtVu1qAoq12ucvFHNwCcVTV7hpWjeY1Hztb"
	raw, _ = json.Marshal(v)
	os.WriteFile(path, raw, 0o600)
	if _, err := OpenVault(path, "correct horse"); err == nil {
		t.Fatal("a vault with a swapped address must not open")
	}
}
