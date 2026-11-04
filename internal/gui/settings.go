package gui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/profile"
	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

var timeNow = time.Now

func keyOf(market, symbol string) loop.PriceKey { return loop.PriceKey{Market: market, Symbol: symbol} }

// Bounds of the timing settings, in seconds.
const (
	MinApprovalSeconds = 3
	MaxApprovalSeconds = 60
	MinMaxHoldSeconds  = 5
	MaxMaxHoldSeconds  = 600
)

func (u *UI) showSettings() {
	st := u.be.Settings()
	riskTol := u.snap.BaseProfile.RiskTolerance
	if riskTol == 0 {
		riskTol = 5
	}
	riskLabel := widget.NewLabel("")
	riskLabel.Wrapping = fyne.TextWrapWord
	risk := widget.NewSlider(profile.MinRiskTolerance, profile.MaxRiskTolerance)
	risk.Step = 1
	risk.OnChanged = func(v float64) {
		riskLabel.SetText(fmt.Sprintf("%d/10 · strategies that ever lost more than %.0f%% are rejected; positions close at −%.1f%%.", int(v), v*5, v*2.5))
	}
	risk.SetValue(float64(riskTol))

	approval := widget.NewEntry()
	approval.SetText(strconv.Itoa(st.ApprovalSeconds))
	maxHold := widget.NewEntry()
	maxHold.SetText(strconv.Itoa(st.MaxHoldSeconds))
	maxTrade := widget.NewEntry()
	maxTrade.SetText(strconv.FormatFloat(float64(st.MaxTradeCents)/100, 'f', -1, 64))

	rpc := widget.NewEntry()
	rpc.SetPlaceHolder(wallet.DefaultSolanaRPC)
	rpc.SetText(st.SolanaRPC)
	devRPC := widget.NewEntry()
	devRPC.SetPlaceHolder(wallet.DevnetRPC)
	devRPC.SetText(st.DevnetRPC)

	folder := widget.NewLabel(u.be.Folder())
	folder.Wrapping = fyne.TextWrapBreak
	folder.Importance = widget.LowImportance

	items := []*widget.FormItem{
		{Text: "Risk tolerance", Widget: container.NewVBox(risk, riskLabel)},
		{Text: "Approval window (s)", Widget: approval, HintText: fmt.Sprintf("How long an opportunity waits for your answer (%d–%d s).", MinApprovalSeconds, MaxApprovalSeconds)},
		{Text: "Max hold (s)", Widget: maxHold, HintText: fmt.Sprintf("A position is closed automatically after this time (%d–%d s).", MinMaxHoldSeconds, MaxMaxHoldSeconds)},
		{Text: "Max per trade ($)", Widget: maxTrade, HintText: "The most the bot puts in one trade, whatever the ledger holds."},
		{Text: "Solana RPC", Widget: rpc, HintText: "Optional: your own mainnet endpoint for the public wallet."},
		{Text: "Devnet RPC", Widget: devRPC, HintText: "Optional: your own devnet endpoint."},
		{Text: "Files", Widget: folder},
	}
	if u.demo {
		reset := widget.NewButton("Reset the demo ledger", func() {
			confirm(u, "Reset the demo ledger?", "The demo positions are closed and the ledger restarts from the profile budget.", func() {
				go func() {
					if err := u.be.SyncLedger(); err != nil {
						fyne.Do(func() { showError(u, err) })
					}
				}()
			})
		})
		items = append(items[:4], append([]*widget.FormItem{{Text: "", Widget: reset}}, items[4:]...)...)
	}
	d := dialog.NewForm("Settings", "Save", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		ap, mh, err := readTiming(approval.Text, maxHold.Text)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		mt, err := parseMoney(maxTrade.Text)
		if err != nil || mt < 100 {
			dialog.ShowError(errors.New("max per trade: an amount of at least $1"), u.win)
			return
		}
		r, main, dev := int(risk.Value), rpc.Text, devRPC.Text
		timingChanged := ap != st.ApprovalSeconds || mh != st.MaxHoldSeconds
		go func() {
			errs := []error{u.be.SetRiskTolerance(r), u.be.SetRPCs(main, dev)}
			if mt != st.MaxTradeCents {
				errs = append(errs, u.be.SetMaxTrade(mt))
			}
			if timingChanged {
				errs = append(errs, u.be.SetTiming(ap, mh))
			}
			err := errors.Join(errs...)
			fyne.Do(func() {
				u.settings = u.be.Settings()
				if err != nil {
					dialog.ShowError(err, u.win)
				}
			})
		}()
	}, u.win)
	d.Resize(fyne.NewSize(580, 560))
	d.Show()
}

// readTiming parses the approval window and the max hold, in seconds.
func readTiming(approval, maxHold string) (int, int, error) {
	ap, err := strconv.Atoi(strings.TrimSpace(approval))
	if err != nil || ap < MinApprovalSeconds || ap > MaxApprovalSeconds {
		return 0, 0, fmt.Errorf("approval window: a whole number of seconds between %d and %d", MinApprovalSeconds, MaxApprovalSeconds)
	}
	mh, err := strconv.Atoi(strings.TrimSpace(maxHold))
	if err != nil || mh < MinMaxHoldSeconds || mh > MaxMaxHoldSeconds {
		return 0, 0, fmt.Errorf("max hold: a whole number of seconds between %d and %d", MinMaxHoldSeconds, MaxMaxHoldSeconds)
	}
	return ap, mh, nil
}
