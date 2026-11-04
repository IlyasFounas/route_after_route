package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/gui"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

// The two accounts of devnet mode:
//   - the main account is the user's own wallet, imported from its secret
//     and kept encrypted (paths.MainVault). Its key is in memory only while
//     unlocked, and only ever signs transfers to the bot account;
//   - the bot account is the app-generated key (paths.DevnetKey). The bot
//     only trades the ledger funded from it, so it can never spend more than
//     what the user moved there.
//
// Transfers are devnet only (wallet.Transfer checks the cluster).

const (
	transferTimeout = 90 * time.Second
	defaultMaxTrade = 2000 // $20, in cents
	minMaxTrade     = 100  // $1
)

// loadMainLocked reads the address of the imported main account, if any.
func (s *session) loadMainLocked() error {
	addr, err := wallet.VaultAddress(s.paths.MainVault)
	if errors.Is(err, os.ErrNotExist) {
		s.mainAddr = ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("main account: %w", err)
	}
	s.mainAddr = addr
	return nil
}

func (s *session) lockMainLocked() {
	if s.mainKey != nil {
		clear(s.mainKey.Private)
		s.mainKey = nil
	}
}

// ImportMain saves the main account's secret, encrypted with password, and
// keeps it unlocked. It replaces a previous main account.
func (s *session) ImportMain(secret, password string, account uint32) (string, error) {
	kp, err := wallet.ParseSecret(secret, account)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.botKey != nil && kp.Address() == s.botKey.Address() {
		return "", errors.New("this is the bot account's own key: import your own wallet")
	}
	if err := wallet.SaveVault(s.paths.MainVault, kp, password); err != nil {
		return "", err
	}
	s.lockMainLocked()
	s.mainAddr, s.mainKey = kp.Address(), &kp
	s.kickWallet()
	return kp.Address(), nil
}

// UnlockMain decrypts the main account's key for this session.
func (s *session) UnlockMain(password string) error {
	s.mu.Lock()
	path := s.paths.MainVault
	s.mu.Unlock()
	kp, err := wallet.OpenVault(path, password) // slow on purpose: not under the lock
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockMainLocked()
	s.mainAddr, s.mainKey = kp.Address(), &kp
	s.kickWallet()
	return nil
}

// LockMain forgets the decrypted key; the vault stays on disk.
func (s *session) LockMain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockMainLocked()
	s.kickWallet()
}

// ForgetMain deletes the main account's vault.
func (s *session) ForgetMain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockMainLocked()
	if err := os.Remove(s.paths.MainVault); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.mainAddr = ""
	s.kickWallet()
	return nil
}

// accounts returns what a transfer needs, checked.
func (s *session) accounts() (main *wallet.Keypair, mainAddr string, bot *wallet.Keypair, rpc string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.fixed || s.settings.WalletMode != gui.ModeDevnet:
		err = errors.New("switch the wallet card to devnet first")
	case s.botKey == nil:
		err = errors.New("the bot account is not loaded")
	case s.mainAddr == "":
		err = errors.New("import your main account first")
	}
	if s.mainKey != nil {
		k := *s.mainKey
		k.Private = append([]byte(nil), s.mainKey.Private...)
		main = &k
	}
	return main, s.mainAddr, s.botKey, s.devnetRPCLocked(), err
}

// solCents is the value of lamports at the real SOL price, in cents.
func (s *session) solCents(ctx context.Context, lamports uint64) (int64, error) {
	px, err := s.pub.Price(ctx, "SOLUSDT")
	if err != nil {
		return 0, fmt.Errorf("SOL price: %w", err)
	}
	return int64(math.Round(float64(lamports) / wallet.LamportsPerSOL * px * 100)), nil
}

// TransferFee estimates the network fee of a transfer between the two
// accounts, in lamports.
func (s *session) TransferFee(toBot bool, lamports uint64) (uint64, error) {
	main, mainAddr, bot, rpc, err := s.accounts()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	sol := wallet.NewSolana(rpc)
	if toBot {
		if main == nil {
			return 0, errors.New("unlock your main account first")
		}
		defer clear(main.Private)
		return sol.TransferFee(ctx, *main, bot.Address(), lamports)
	}
	return sol.TransferFee(ctx, *bot, mainAddr, lamports)
}

// FundBot moves lamports from the main account to the bot account, then
// adds their value to the ledger. A ledger never funded starts from the bot
// account's whole value instead (as Sync ledger does).
func (s *session) FundBot(lamports uint64) (string, error) {
	main, _, bot, rpc, err := s.accounts()
	if err != nil {
		return "", err
	}
	if main == nil {
		return "", errors.New("unlock your main account first: its password is needed to sign")
	}
	defer clear(main.Private)
	ctx, cancel := context.WithTimeout(s.ctx, transferTimeout)
	defer cancel()
	sol := wallet.NewSolana(rpc)
	if bal, err := sol.Balance(ctx, bot.Address()); err == nil && bal == 0 {
		if min, err := sol.RentExemptMinimum(ctx); err == nil && lamports < min {
			return "", fmt.Errorf("a new account must receive at least %s SOL", solString(min))
		}
	}
	cents, err := s.solCents(ctx, lamports)
	if err != nil {
		return "", err
	}
	sig, err := sol.Transfer(ctx, *main, bot.Address(), lamports)
	if err != nil {
		return sig, err
	}
	s.mu.Lock()
	funded := s.settings.LedgerCapital >= minLedger
	s.mu.Unlock()
	if !funded {
		s.kickWallet()
		return sig, s.SyncLedger()
	}
	if err := s.eng.Deposit(ctx, cents); err != nil {
		return sig, fmt.Errorf("the SOL arrived, but the ledger was not credited (%w): press Sync ledger", err)
	}
	s.mu.Lock()
	s.settings.LedgerCapital += cents
	err = s.saveSettingsLocked()
	s.mu.Unlock()
	s.kickWallet()
	return sig, err
}

// WithdrawToMain moves lamports from the bot account back to the main
// account. Their value is first taken from the ledger's free cash (the money
// not in open positions), and given back if the transfer fails. The main
// account may stay locked: only its address is needed.
func (s *session) WithdrawToMain(lamports uint64) (string, error) {
	_, mainAddr, bot, rpc, err := s.accounts()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(s.ctx, transferTimeout)
	defer cancel()
	cents, err := s.solCents(ctx, lamports)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	funded := s.settings.LedgerCapital >= minLedger
	s.mu.Unlock()
	if funded {
		if err := s.eng.Deposit(ctx, -cents); err != nil {
			return "", fmt.Errorf("%w: close positions or withdraw less", err)
		}
	}
	sig, err := wallet.NewSolana(rpc).Transfer(ctx, *bot, mainAddr, lamports)
	if err != nil {
		if funded {
			if rerr := s.eng.Deposit(context.WithoutCancel(ctx), cents); rerr != nil {
				err = errors.Join(err, fmt.Errorf("the ledger could not be credited back: press Sync ledger (%w)", rerr))
			}
		}
		return sig, err
	}
	if funded {
		s.mu.Lock()
		s.settings.LedgerCapital = max(0, s.settings.LedgerCapital-cents)
		err = s.saveSettingsLocked()
		s.mu.Unlock()
	}
	s.kickWallet()
	return sig, err
}

// SetMaxTrade changes the most the bot may put in one trade, in cents.
func (s *session) SetMaxTrade(cents int64) error {
	if cents < minMaxTrade {
		return fmt.Errorf("the max per trade is at least %s", usdString(minMaxTrade))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fixed && s.settings.LedgerCapital >= minLedger && cents > s.settings.LedgerCapital {
		return fmt.Errorf("the max per trade (%s) cannot exceed the bot's ledger (%s)", usdString(cents), usdString(s.settings.LedgerCapital))
	}
	s.settings.MaxTradeCents = cents
	if s.eng != nil && !s.fixed {
		s.eng.SetMaxTrade(cents)
	}
	return s.saveSettingsLocked()
}

func solString(lamports uint64) string {
	return fmt.Sprintf("%.6g", float64(lamports)/wallet.LamportsPerSOL)
}
