package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// MinPasswordLength is the shortest vault password accepted.
const MinPasswordLength = 8

// vaultIterations is the PBKDF2 cost (OWASP 2023 for HMAC-SHA256). Tests
// lower it.
var vaultIterations = 600_000

// ErrWrongPassword is returned when the vault does not open.
var ErrWrongPassword = errors.New("wrong password")

// vaultFile is the on-disk format. The address is in clear, so that the
// balances can be shown while the vault is locked; it is also bound to the
// ciphertext (GCM additional data), so it cannot be swapped.
type vaultFile struct {
	Version    int    `json:"version"`
	Address    string `json:"address"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func vaultKey(password string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, iter, 32)
}

// SaveVault encrypts the key pair with password (AES-256-GCM, key derived
// with PBKDF2-HMAC-SHA256) and writes it to path with mode 0600, replacing
// any previous vault atomically.
func SaveVault(path string, kp Keypair, password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("the password needs at least %d characters", MinPasswordLength)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := vaultKey(password, salt, vaultIterations)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	v := vaultFile{Version: 1, Address: kp.Address(), KDF: "pbkdf2-sha256", Iterations: vaultIterations, Salt: salt, Nonce: nonce}
	v.Ciphertext = gcm.Seal(nil, nonce, kp.Private, []byte(v.Address))
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vault-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readVault(path string) (vaultFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return vaultFile{}, err
	}
	var v vaultFile
	if err := json.Unmarshal(raw, &v); err != nil {
		return vaultFile{}, fmt.Errorf("%s: not a wallet vault: %w", path, err)
	}
	if v.Version != 1 || v.KDF != "pbkdf2-sha256" || v.Iterations < 1 {
		return vaultFile{}, fmt.Errorf("%s: unsupported vault (version %d, %s)", path, v.Version, v.KDF)
	}
	if err := ValidateSolanaAddress(v.Address); err != nil {
		return vaultFile{}, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// VaultAddress returns the public address of the vault at path, without
// the password.
func VaultAddress(path string) (string, error) {
	v, err := readVault(path)
	return v.Address, err
}

// OpenVault decrypts the vault at path.
func OpenVault(path, password string) (Keypair, error) {
	v, err := readVault(path)
	if err != nil {
		return Keypair{}, err
	}
	key, err := vaultKey(password, v.Salt, v.Iterations)
	if err != nil {
		return Keypair{}, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Keypair{}, err
	}
	if len(v.Nonce) != gcm.NonceSize() {
		return Keypair{}, fmt.Errorf("%s: corrupted vault", path)
	}
	plain, err := gcm.Open(nil, v.Nonce, v.Ciphertext, []byte(v.Address))
	if err != nil {
		return Keypair{}, ErrWrongPassword
	}
	if len(plain) != ed25519.PrivateKeySize {
		return Keypair{}, fmt.Errorf("%s: corrupted vault", path)
	}
	kp, err := keypairFromSecret(plain)
	if err != nil {
		return Keypair{}, fmt.Errorf("%s: corrupted vault", path)
	}
	if kp.Address() != v.Address {
		return Keypair{}, fmt.Errorf("%s: the key does not match the vault address", path)
	}
	return kp, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
