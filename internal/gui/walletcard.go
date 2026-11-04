package gui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

const maxWalletRows = 8

func (u *UI) applyWallet(w WalletView) {
	u.wal = w
	var objs []fyne.CanvasObject
	if !u.demo {
		objs = append(objs, u.modeSwitch())
	}
	if w.Devnet {
		objs = append(objs, u.devnetWallet(w)...)
		if m := w.Main; m != nil && m.Locked && !u.unlockAsked && !u.hidden {
			// Once per session: the main account signs the bot's funding.
			u.unlockAsked = true
			u.showUnlock(nil)
		}
	} else {
		objs = append(objs, u.publicWallet(w)...)
	}
	u.walletBox.Objects = objs
	u.walletBox.Refresh()
	if u.rightBox != nil {
		u.rightBox.Refresh() // the card height changed: lay the column out again
	}
}

// modeSwitch chooses between the public wallet (read-only, advice) and the
// devnet wallet (the bot may trade, test SOL).
func (u *UI) modeSwitch() fyne.CanvasObject {
	btn := func(label, mode string) *widget.Button {
		b := widget.NewButton(label, func() { u.setWalletMode(mode) })
		b.Importance = widget.LowImportance
		if u.settings.WalletMode == mode {
			b.Importance = widget.HighImportance
		}
		return b
	}
	return container.NewGridWithColumns(2, btn("Public wallet", ModePublic), btn("Devnet", ModeDevnet))
}

func (u *UI) setWalletMode(mode string) {
	if mode == u.settings.WalletMode {
		return
	}
	do := func() {
		go func() {
			err := u.be.SetWalletMode(mode)
			fyne.Do(func() {
				if err != nil {
					showError(u, err)
					return
				}
				u.settings = u.be.Settings()
				u.applyWallet(WalletView{Devnet: mode == ModeDevnet, Address: u.settings.Wallet,
					Loading: mode == ModeDevnet || u.settings.Wallet != ""})
				u.refreshMode()
			})
		}()
	}
	if mode == ModePublic && len(u.snap.State.Positions) > 0 {
		confirm(u, "Leave devnet?", "The bot's open devnet positions are closed at the current price, and the bot stops trading.", do)
		return
	}
	do()
}

func (u *UI) publicWallet(w WalletView) []fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)
	title := text("PUBLIC WALLET · READ-ONLY", 11, true, muted)
	if w.Address == "" {
		intro := widget.NewLabel("Import a Solana wallet to follow its value here. Read-only: only the public address is needed, never your secret phrase. The bot never trades with it.")
		intro.Wrapping = fyne.TextWrapWord
		btn := widget.NewButtonWithIcon("Import a Solana wallet", theme.ContentAddIcon(), u.showImport)
		btn.Importance = widget.HighImportance
		return []fyne.CanvasObject{title, intro, btn}
	}
	objs := u.walletHeader(title)
	objs = append(objs, u.walletBody(w, fg, muted)...)
	change := widget.NewButton("Change", u.showImport)
	change.Importance = widget.LowImportance
	remove := widget.NewButton("Remove", func() {
		confirm(u, "Remove the wallet?", "The app will stop following this address.", func() {
			u.applyWallet(WalletView{})
			go u.be.SetWallet("")
		})
	})
	remove.Importance = widget.LowImportance
	return append(objs, container.NewHBox(change, remove))
}

func (u *UI) walletHeader(title fyne.CanvasObject) []fyne.CanvasObject {
	refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() { go u.be.RefreshWallet() })
	refresh.Importance = widget.LowImportance
	return []fyne.CanvasObject{container.NewHBox(container.NewCenter(title), layout.NewSpacer(), refresh)}
}

// walletBody is the total, the holdings, the address and the error.
func (u *UI) walletBody(w WalletView, fg, muted color.Color) []fyne.CanvasObject {
	var objs []fyne.CanvasObject
	switch {
	case w.Loading && len(w.Holdings) == 0:
		objs = append(objs, text("Reading the wallet…", 15, false, muted))
	default:
		objs = append(objs, text(usd(w.Total), 26, true, fg))
	}
	for i, h := range w.Holdings {
		if i == maxWalletRows {
			more := widget.NewLabel(fmt.Sprintf("and %d more tokens", len(w.Holdings)-maxWalletRows))
			more.Importance = widget.LowImportance
			objs = append(objs, more)
			break
		}
		objs = append(objs, holdingRow(h))
	}
	if !w.Loading && w.Err == "" && len(w.Holdings) == 0 {
		msg := "This wallet is empty."
		if w.Devnet {
			msg = "This wallet is empty: airdrop some devnet SOL."
		}
		empty := widget.NewLabel(msg)
		empty.Importance = widget.LowImportance
		objs = append(objs, empty)
	}
	foot := shortAddress(w.Address)
	if !w.Updated.IsZero() {
		foot += " · updated " + w.Updated.Local().Format("15:04:05")
	}
	footer := widget.NewLabel(foot)
	footer.Importance = widget.LowImportance
	footer.SizeName = theme.SizeNameCaptionText
	objs = append(objs, footer)
	if w.Err != "" {
		e := widget.NewLabel(w.Err)
		e.Importance = widget.DangerImportance
		e.Wrapping = fyne.TextWrapWord
		e.SizeName = theme.SizeNameCaptionText
		objs = append(objs, e)
	}
	return objs
}

func holdingRow(h HoldingView) fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)
	name := h.Name
	if name == "" {
		name = "Unknown token"
	}
	value := "—"
	if h.Priced {
		value = usd(h.Value)
	}
	v := text(value, 14, true, fg)
	v.Alignment = fyne.TextAlignTrailing
	q := text(amount(h.Amount)+" "+h.Symbol, 11, false, muted)
	q.Alignment = fyne.TextAlignTrailing
	return container.NewBorder(nil, nil,
		container.NewCenter(coinBadge(h.Symbol, 30)),
		container.NewVBox(v, q),
		container.NewVBox(text(h.Symbol, 14, true, fg), text(name, 11, false, muted)))
}

func (u *UI) showImport() {
	addr := widget.NewEntry()
	addr.SetPlaceHolder("e.g. 7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU")
	addr.SetText(u.wal.Address)
	addr.Validator = func(s string) error { return wallet.ValidateSolanaAddress(s) }
	item := widget.NewFormItem("Public address", addr)
	item.HintText = "Copy it from Phantom, Solflare or Backpack. Never paste a secret phrase or private key."
	d := dialog.NewForm("Import a Solana wallet", "Import", "Cancel", []*widget.FormItem{item}, func(ok bool) {
		if !ok {
			return
		}
		a := addr.Text
		u.applyWallet(WalletView{Address: a, Loading: true})
		go func() {
			if err := u.be.SetWallet(a); err != nil {
				fyne.Do(func() { dialog.ShowError(err, u.win) })
			}
		}()
	}, u.win)
	d.Resize(fyne.NewSize(520, 220))
	d.Show()
}
