package gui

import (
	"context"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

// Wallet modes.
const (
	// ModePublic follows a PUBLIC mainnet address, read-only. The bot never
	// trades: the algorithm only advises and notifies.
	ModePublic = "public"
	// ModeDevnet uses the app-generated devnet wallet. Approved entries are
	// executed at the real Binance price in a local ledger funded from that
	// wallet's value.
	ModeDevnet = "devnet"
)

// MaxFavorites is the number of cryptos watched at the same time.
const MaxFavorites = 10

// Settings are the user's choices in the app, saved between launches.
type Settings struct {
	// Pair is the crypto shown on the chart, as a Binance pair (SOLUSDT).
	// It is always one of the favorites.
	Pair string `json:"pair"`
	// Favorites are the cryptos the algorithm watches, all at once.
	Favorites []string `json:"favorites,omitempty"`
	// Range is the period shown on the chart (see Ranges).
	Range string `json:"range"`
	// Execute allows the bot to take the entries the user approves.
	Execute bool `json:"execute"`
	// WalletMode is ModePublic or ModeDevnet.
	WalletMode string `json:"wallet_mode,omitempty"`
	// Wallet is a PUBLIC Solana mainnet address, read-only (public mode).
	Wallet string `json:"wallet,omitempty"`
	// SolanaRPC overrides the public Solana mainnet endpoint.
	SolanaRPC string `json:"solana_rpc,omitempty"`
	// DevnetRPC overrides the public Solana devnet endpoint.
	DevnetRPC string `json:"devnet_rpc,omitempty"`
	// LedgerCapital is the devnet ledger's starting value in US cents, taken
	// from the devnet wallet at the last sync (0 = never synced).
	LedgerCapital int64 `json:"ledger_capital,omitempty"`
	// ApprovalSeconds is how long an opportunity waits for the user.
	ApprovalSeconds int `json:"approval_seconds,omitempty"`
	// MaxHoldSeconds closes a position held this long.
	MaxHoldSeconds int `json:"max_hold_seconds,omitempty"`
	// MaxTradeCents is the most the bot puts in one trade, in US cents.
	MaxTradeCents int64 `json:"max_trade_cents,omitempty"`
	// Effort is the performance level (see live.Efforts).
	Effort string `json:"effort,omitempty"`
}

// Coin is a crypto the user can follow.
type Coin struct {
	Pair   string  // SOLUSDT
	Base   string  // SOL
	Price  float64 // USD
	Change float64 // over 24 h, fraction
}

// HoldingView is a wallet balance with its value in dollars.
type HoldingView struct {
	wallet.Holding
	Price  float64
	Value  float64
	Priced bool
}

// WalletView is the state of the wallet shown in the wallet card. In devnet
// mode, it is the bot account, and Main is the user's main account.
type WalletView struct {
	Devnet   bool // the app's devnet wallet (test SOL, no value)
	Main     *WalletView
	Locked   bool // main account: its key is not decrypted
	Address  string
	Holdings []HoldingView
	Total    float64
	Updated  time.Time
	Loading  bool
	Err      string
}

// Backend is what the window needs from the application. Methods may block
// (network, engine restart): the UI calls them off the Fyne goroutine.
type Backend interface {
	Snapshots() <-chan live.Snapshot
	Wallets() <-chan WalletView
	Settings() Settings
	// Demo is true for the offline replay (--demo / --config): its ledger
	// is the profile budget and the wallet modes do not apply.
	Demo() bool
	SetRange(r string)
	SetExecution(on bool) error

	// SetPair shows pair on the chart, adding it to the favorites if needed.
	SetPair(pair string) error
	// SetFavorites replaces the watched cryptos.
	SetFavorites(pairs []string) error
	Coins(ctx context.Context) ([]Coin, error)
	// Logo returns the PNG logo of a crypto (e.g. SOL).
	Logo(ctx context.Context, base string) ([]byte, error)
	// History returns the curve of pair over a long range (1D, 1W, 1M).
	// Nil without error: use the engine series.
	History(ctx context.Context, pair, rng string) ([]Point, error)

	// Approve and Reject answer an opportunity.
	Approve(id string)
	Reject(id string)

	SetWalletMode(mode string) error
	SetWallet(address string) error
	// Airdrop asks the devnet faucet for 1 test SOL, for the main account
	// or the bot account.
	Airdrop(toMain bool) error

	// ImportMain saves the user's main account (recovery phrase, base58
	// private key or Solana CLI keypair), encrypted with password, and
	// keeps it unlocked. It returns its address.
	ImportMain(secret, password string, account uint32) (string, error)
	UnlockMain(password string) error
	LockMain()
	ForgetMain() error
	// TransferFee estimates the network fee of a transfer, in lamports.
	TransferFee(toBot bool, lamports uint64) (uint64, error)
	// FundBot moves devnet SOL from the main account to the bot account
	// and credits the ledger; WithdrawToMain does the reverse, from the
	// ledger's free cash. Both return the transaction signature.
	FundBot(lamports uint64) (string, error)
	WithdrawToMain(lamports uint64) (string, error)
	// SetMaxTrade changes the most the bot puts in one trade, in cents.
	SetMaxTrade(cents int64) error
	// SyncLedger restarts the ledger from the devnet wallet's value (the
	// profile budget in demo).
	SyncLedger() error
	RefreshWallet()

	SetRiskTolerance(r int) error
	SetTiming(approvalSeconds, maxHoldSeconds int) error
	SetRPCs(mainnet, devnet string) error
	// SetEffort changes the performance level, live. The note, when not
	// empty, tells what only applies at the next launch.
	SetEffort(e live.Effort) (note string, err error)
	Folder() string
	Quit()
}
