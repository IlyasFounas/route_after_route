package wallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Genesis hashes of the Solana test clusters: the only ones Transfer
// accepts. Anything else (mainnet, an unknown cluster) is refused.
const (
	DevnetGenesis  = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG"
	TestnetGenesis = "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY"
	MainnetGenesis = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"
)

// ErrNotTestCluster is returned when a transfer is asked on a cluster that
// is not devnet or testnet.
var ErrNotTestCluster = errors.New("transfers are only allowed on devnet (or testnet): this endpoint is another cluster")

// confirmEvery is the polling period of getSignatureStatuses.
var confirmEvery = time.Second

// systemProgram is 11111111111111111111111111111111: 32 zero bytes.
var systemProgram = make([]byte, 32)

// Balance returns the SOL balance of address, in lamports.
func (s *Solana) Balance(ctx context.Context, address string) (uint64, error) {
	if err := ValidateSolanaAddress(address); err != nil {
		return 0, err
	}
	var bal struct {
		Value uint64 `json:"value"`
	}
	err := s.call(ctx, "getBalance", []any{strings.TrimSpace(address), map[string]string{"commitment": "confirmed"}}, &bal)
	return bal.Value, err
}

// RentExemptMinimum is the smallest balance an empty account may keep.
func (s *Solana) RentExemptMinimum(ctx context.Context) (uint64, error) {
	var v uint64
	err := s.call(ctx, "getMinimumBalanceForRentExemption", []any{0}, &v)
	return v, err
}

// requireTestCluster refuses any cluster but devnet and testnet, whatever
// the endpoint's URL says.
func (s *Solana) requireTestCluster(ctx context.Context) error {
	if strings.Contains(s.RPC, "mainnet") {
		return ErrNotTestCluster
	}
	var genesis string
	if err := s.call(ctx, "getGenesisHash", []any{}, &genesis); err != nil {
		return err
	}
	if genesis != DevnetGenesis && genesis != TestnetGenesis {
		return ErrNotTestCluster
	}
	return nil
}

// transferMessage is the legacy message of one SystemProgram transfer:
// header, the three accounts (payer, recipient, system program), the recent
// blockhash and the instruction (index 2, then the lamports, little endian).
func transferMessage(from, to, blockhash []byte, lamports uint64) []byte {
	msg := []byte{1, 0, 1} // 1 signer, 0 read-only signed, 1 read-only unsigned
	msg = append(msg, 3)   // compact-u16: 3 account keys
	msg = append(msg, from...)
	msg = append(msg, to...)
	msg = append(msg, systemProgram...)
	msg = append(msg, blockhash...)
	msg = append(msg, 1)       // 1 instruction
	msg = append(msg, 2)       // program: account 2 (system)
	msg = append(msg, 2, 0, 1) // 2 accounts: from, to
	msg = append(msg, 12)      // data length
	msg = binary.LittleEndian.AppendUint32(msg, 2)
	msg = binary.LittleEndian.AppendUint64(msg, lamports)
	return msg
}

// signedTransaction is the wire transaction: one signature, then msg.
func signedTransaction(kp Keypair, msg []byte) []byte {
	tx := []byte{1}
	tx = append(tx, ed25519.Sign(kp.Private, msg)...)
	return append(tx, msg...)
}

// TransferFee is the network fee of a transfer, in lamports.
func (s *Solana) TransferFee(ctx context.Context, from Keypair, to string, lamports uint64) (uint64, error) {
	msg, _, err := s.prepareTransfer(ctx, from, to, lamports)
	if err != nil {
		return 0, err
	}
	var res struct {
		Value *uint64 `json:"value"`
	}
	if err := s.call(ctx, "getFeeForMessage", []any{base64.StdEncoding.EncodeToString(msg), map[string]string{"commitment": "confirmed"}}, &res); err != nil {
		return 0, err
	}
	if res.Value == nil {
		return 0, errors.New("the network did not price the transfer: try again")
	}
	return *res.Value, nil
}

func (s *Solana) prepareTransfer(ctx context.Context, from Keypair, to string, lamports uint64) ([]byte, uint64, error) {
	if lamports == 0 {
		return nil, 0, errors.New("nothing to transfer")
	}
	toKey, err := DecodeBase58(strings.TrimSpace(to))
	if err != nil || len(toKey) != 32 {
		return nil, 0, fmt.Errorf("invalid recipient address %q", to)
	}
	fromKey := from.Private.Public().(ed25519.PublicKey)
	if string(fromKey) == string(toKey) {
		return nil, 0, errors.New("the sender and the recipient are the same account")
	}
	if err := s.requireTestCluster(ctx); err != nil {
		return nil, 0, err
	}
	var bh struct {
		Value struct {
			Blockhash            string `json:"blockhash"`
			LastValidBlockHeight uint64 `json:"lastValidBlockHeight"`
		} `json:"value"`
	}
	if err := s.call(ctx, "getLatestBlockhash", []any{map[string]string{"commitment": "confirmed"}}, &bh); err != nil {
		return nil, 0, err
	}
	hash, err := DecodeBase58(bh.Value.Blockhash)
	if err != nil || len(hash) != 32 {
		return nil, 0, fmt.Errorf("invalid blockhash %q", bh.Value.Blockhash)
	}
	return transferMessage(fromKey, toKey, hash, lamports), bh.Value.LastValidBlockHeight, nil
}

// Transfer sends lamports from one key to an address, on devnet or testnet
// only, and waits until the network confirms it. It returns the signature.
func (s *Solana) Transfer(ctx context.Context, from Keypair, to string, lamports uint64) (string, error) {
	msg, lastValid, err := s.prepareTransfer(ctx, from, to, lamports)
	if err != nil {
		return "", err
	}
	tx := signedTransaction(from, msg)
	var sig string
	opts := map[string]string{"encoding": "base64", "preflightCommitment": "confirmed"}
	if err := s.call(ctx, "sendTransaction", []any{base64.StdEncoding.EncodeToString(tx), opts}, &sig); err != nil {
		return "", err
	}
	if want := EncodeBase58(tx[1:65]); sig != want {
		return "", fmt.Errorf("the network returned signature %s for transaction %s", sig, want)
	}
	return sig, s.confirm(ctx, sig, lastValid)
}

// confirm waits until sig is confirmed, fails, or its blockhash expires.
func (s *Solana) confirm(ctx context.Context, sig string, lastValid uint64) error {
	t := time.NewTicker(confirmEvery)
	defer t.Stop()
	for {
		var res struct {
			Value []*struct {
				ConfirmationStatus string `json:"confirmationStatus"`
				Err                any    `json:"err"`
			} `json:"value"`
		}
		if err := s.call(ctx, "getSignatureStatuses", []any{[]string{sig}}, &res); err != nil {
			return err
		}
		if len(res.Value) == 1 && res.Value[0] != nil {
			st := res.Value[0]
			if st.Err != nil {
				return fmt.Errorf("transaction %s failed: %v", sig, st.Err)
			}
			if st.ConfirmationStatus == "confirmed" || st.ConfirmationStatus == "finalized" {
				return nil
			}
		} else {
			var height uint64
			if err := s.call(ctx, "getBlockHeight", []any{map[string]string{"commitment": "confirmed"}}, &height); err == nil && height > lastValid {
				return fmt.Errorf("transaction %s expired before confirmation: nothing was sent", sig)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("transaction %s not confirmed yet: check it on the explorer (%w)", sig, ctx.Err())
		case <-t.C:
		}
	}
}

// ExplorerURL is the Solana Explorer page of a devnet transaction.
func ExplorerURL(sig string) string {
	return "https://explorer.solana.com/tx/" + sig + "?cluster=devnet"
}
