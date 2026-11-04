package gui

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/wallet"
)

// devnetWallet is the wallet card in devnet mode: the user's main account
// on top, the bot account under it, and the transfers between them.
func (u *UI) devnetWallet(w WalletView) []fyne.CanvasObject {
	objs := u.mainAccount(w.Main)
	sep := canvas.NewRectangle(themeColor(theme.ColorNameSeparator))
	sep.SetMinSize(fyne.NewSize(0, 1))
	objs = append(objs, sep)
	return append(objs, u.botAccount(w)...)
}

func (u *UI) mainAccount(m *WalletView) []fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)
	title := text("MAIN ACCOUNT · YOUR WALLET", 11, true, muted)
	if m == nil {
		intro := widget.NewLabel("Import the wallet you put your money in. The bot never trades it: you move a small amount to the bot account below, and the bot only uses that.")
		intro.Wrapping = fyne.TextWrapWord
		btn := widget.NewButtonWithIcon("Import your wallet", theme.ContentAddIcon(), u.showImportMain)
		btn.Importance = widget.HighImportance
		return []fyne.CanvasObject{title, intro, btn}
	}
	objs := u.walletHeader(title)
	objs = append(objs, u.walletBody(*m, fg, muted)...)
	lock := widget.NewButtonWithIcon("Unlock", theme.VisibilityIcon(), func() { u.showUnlock(nil) })
	if !m.Locked {
		lock = widget.NewButtonWithIcon("Lock", theme.VisibilityOffIcon(), func() { go u.be.LockMain() })
	}
	lock.Importance = widget.LowImportance
	airdrop := u.airdropButton(true)
	forget := widget.NewButton("Forget", func() {
		confirm(u, "Forget the main account?",
			"Its encrypted key is deleted from this computer. Your wallet itself is untouched: you can import it again with its recovery phrase.", func() {
				go func() {
					err := u.be.ForgetMain()
					fyne.Do(func() {
						if err != nil {
							showError(u, err)
						}
					})
				}()
			})
	})
	forget.Importance = widget.LowImportance
	state := "Locked: unlock it to fund the bot."
	if !m.Locked {
		state = "Unlocked for this session."
	}
	hint := caption(state)
	return append(objs, container.NewGridWithColumns(3, lock, airdrop, forget), hint)
}

func (u *UI) botAccount(w WalletView) []fyne.CanvasObject {
	fg := themeColor(theme.ColorNameForeground)
	muted := themeColor(colorMuted)
	objs := []fyne.CanvasObject{text("BOT ACCOUNT · THE ONLY MONEY THE BOT USES", 11, true, muted)}
	objs = append(objs, u.walletBody(w, fg, muted)...)
	fund := widget.NewButtonWithIcon("Fund bot", theme.MoveDownIcon(), func() { u.startTransfer(true) })
	fund.Importance = widget.HighImportance
	withdraw := widget.NewButtonWithIcon("Withdraw", theme.MoveUpIcon(), func() { u.startTransfer(false) })
	copyBtn := widget.NewButtonWithIcon("Copy address", theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(w.Address)
	})
	copyBtn.Importance = widget.LowImportance
	sync := widget.NewButtonWithIcon("Reset ledger", theme.ViewRefreshIcon(), func() {
		confirm(u, "Reset the bot ledger?", u.resetLedgerText(w), func() {
			go func() {
				err := u.be.SyncLedger()
				fyne.Do(func() {
					if err != nil {
						showError(u, err)
						return
					}
					u.settings = u.be.Settings()
					u.refreshMode()
				})
			}()
		})
	})
	sync.Importance = widget.LowImportance
	hint := caption(fmt.Sprintf("The bot trades its ledger (see BOT LEDGER above), at most %s per trade. Devnet has no real exchange, so trades are simulated at the real Binance price: they change the ledger, not the SOL of this account. Fund bot and Withdraw are real devnet transfers, and update the ledger by themselves.",
		usdCents(u.settings.MaxTradeCents)))
	if w.Main == nil {
		hint = caption("Import your main account to fund the bot. You can also airdrop test SOL here directly.")
		return append(objs, container.NewGridWithColumns(2, u.airdropButton(false), sync), copyBtn, hint)
	}
	return append(objs, container.NewGridWithColumns(2, fund, withdraw), container.NewGridWithColumns(2, copyBtn, sync), hint)
}

// resetLedgerText explains Reset ledger with the two numbers it reconciles.
func (u *UI) resetLedgerText(bot WalletView) string {
	ledger := "not funded yet"
	if u.hasSnap && u.settings.LedgerCapital > 0 {
		ledger = usdCents(u.snap.Equity)
	}
	open := ""
	if n := len(u.snap.State.Positions); n > 0 {
		open = fmt.Sprintf("\n\nIts %d open %s will be dropped, without being recorded as trades.", n, plural(n, "position", "positions"))
	}
	return fmt.Sprintf("The ledger is the money the bot trades with. Its trades are simulated, so after a while it no longer matches the SOL really in the bot account.\n\n"+
		"Ledger now: %s\nBot account: %s\n\nResetting sets the ledger to the bot account's value.%s The history of closed trades is kept.\n\n"+
		"You only need this to start over: Fund bot and Withdraw already update the ledger.", ledger, usd(bot.Total), open)
}

func caption(s string) *widget.Label {
	l := widget.NewLabel(s)
	l.Wrapping = fyne.TextWrapWord
	l.SizeName = theme.SizeNameCaptionText
	l.Importance = widget.LowImportance
	return l
}

func (u *UI) airdropButton(toMain bool) *widget.Button {
	b := widget.NewButtonWithIcon("Airdrop", theme.DownloadIcon(), nil)
	b.Importance = widget.LowImportance
	b.OnTapped = func() {
		b.Disable()
		go func() {
			err := u.be.Airdrop(toMain)
			fyne.Do(func() {
				b.Enable()
				if err != nil {
					showError(u, err)
				}
			})
		}()
	}
	return b
}

// showImportMain asks for the main account's secret and a password. The
// address it gives is shown before anything is saved, so the user can check
// it is their wallet.
func (u *UI) showImportMain() {
	secret := widget.NewPasswordEntry()
	secret.SetPlaceHolder("12 or 24 words, or a private key")
	account := widget.NewEntry()
	account.SetText("0")
	addr := widget.NewLabel("—")
	addr.TextStyle.Monospace = true
	addr.Wrapping = fyne.TextWrapBreak
	pw := widget.NewPasswordEntry()
	pw.SetPlaceHolder(fmt.Sprintf("at least %d characters", wallet.MinPasswordLength))
	pw2 := widget.NewPasswordEntry()
	checked := widget.NewCheck("This is the address of my wallet", nil)
	checked.Disable()

	var derived string
	preview := func(string) {
		derived = ""
		checked.SetChecked(false)
		checked.Disable()
		n, err := strconv.ParseUint(strings.TrimSpace(account.Text), 10, 31)
		if err != nil {
			addr.SetText("the account number is a whole number (0 for the first one)")
			return
		}
		kp, err := wallet.ParseSecret(secret.Text, uint32(n))
		if err != nil {
			if strings.TrimSpace(secret.Text) == "" {
				addr.SetText("—")
			} else {
				addr.SetText(err.Error())
			}
			return
		}
		derived = kp.Address()
		addr.SetText(derived)
		checked.Enable()
	}
	secret.OnChanged, account.OnChanged = preview, preview

	warn := widget.NewLabel("Use a test wallet made for devnet. The secret is encrypted on this computer with your password, never sent anywhere, and only signs devnet transfers to the bot account.")
	warn.Wrapping = fyne.TextWrapWord
	warn.Importance = widget.WarningImportance
	items := []*widget.FormItem{
		{Text: "Secret", Widget: secret, HintText: "Recovery phrase (Phantom, Solflare, Backpack), base58 private key, or a Solana CLI keypair."},
		{Text: "Account", Widget: account, HintText: "For a recovery phrase: 0 is the wallet's first account."},
		{Text: "Address", Widget: addr},
		{Text: "", Widget: checked},
		{Text: "Password", Widget: pw, HintText: "Asked each time the app starts, to unlock the account."},
		{Text: "Confirm", Widget: pw2},
		{Text: "", Widget: warn},
	}
	var valid func() error
	d := dialog.NewForm("Import your main account", "Import", "Cancel", items, func(ok bool) {
		if !ok {
			secret.SetText("")
			return
		}
		// The form may let Import through before every field was touched.
		if err := valid(); err != nil {
			secret.SetText("")
			showError(u, fmt.Errorf("nothing was imported: %w", err))
			return
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(account.Text), 10, 31)
		s, p := secret.Text, pw.Text
		secret.SetText("")
		go func() {
			_, err := u.be.ImportMain(s, p, uint32(n))
			fyne.Do(func() {
				if err != nil {
					showError(u, err)
				}
			})
		}()
	}, u.win)
	valid = func() error {
		switch {
		case derived == "":
			return errors.New("no address yet")
		case !checked.Checked:
			return errors.New("check the address")
		case len([]rune(pw.Text)) < wallet.MinPasswordLength:
			return fmt.Errorf("the password needs at least %d characters", wallet.MinPasswordLength)
		case pw.Text != pw2.Text:
			return errors.New("the passwords differ")
		}
		return nil
	}
	pw2.Validator = func(string) error { return valid() }
	checked.OnChanged = func(bool) { pw2.SetValidationError(valid()) }
	pw.OnChanged = func(string) { pw2.SetValidationError(valid()) }
	d.Resize(fyne.NewSize(600, 560))
	d.Show()
	u.importDialog = &importDialog{secret: secret, account: account, addr: addr, checked: checked, pw: pw, pw2: pw2, valid: valid}
}

// importDialog keeps the import form's widgets for the tests.
type importDialog struct {
	secret, account, pw, pw2 *widget.Entry
	addr                     *widget.Label
	checked                  *widget.Check
	valid                    func() error
}

// showUnlock asks for the main account's password, then runs then (if any).
func (u *UI) showUnlock(then func()) {
	pw := widget.NewPasswordEntry()
	item := widget.NewFormItem("Password", pw)
	item.HintText = "The password chosen at import. The key stays decrypted until you lock it or quit."
	d := dialog.NewForm("Unlock your main account", "Unlock", "Not now", []*widget.FormItem{item}, func(ok bool) {
		if !ok {
			return
		}
		p := pw.Text
		go func() {
			err := u.be.UnlockMain(p)
			fyne.Do(func() {
				if err != nil {
					showError(u, err)
					return
				}
				if then != nil {
					then()
				}
			})
		}()
	}, u.win)
	d.Resize(fyne.NewSize(460, 200))
	d.Show()
	u.win.Canvas().Focus(pw)
}

// startTransfer opens the amount form of a transfer: main → bot (toBot) or
// bot → main.
func (u *UI) startTransfer(toBot bool) {
	if toBot && u.wal.Main != nil && u.wal.Main.Locked {
		u.showUnlock(func() { u.startTransfer(true) })
		return
	}
	src, dst := u.wal.Main, &u.wal
	if !toBot {
		src, dst = &u.wal, u.wal.Main
	}
	if src == nil || dst == nil {
		return
	}
	price := solPrice(u.wal)
	if m := u.wal.Main; price == 0 && m != nil {
		price = solPrice(*m)
	}
	maxSOL := solBalance(*src)
	limit := ""
	if !toBot {
		// Only the ledger's free cash may leave: the rest is in positions.
		free := u.settings.LedgerCapital
		if u.snap.State.Cycle > 0 {
			free = u.snap.State.Cash
		}
		if price > 0 {
			maxSOL = min(maxSOL, float64(free)/100/price)
			limit = fmt.Sprintf(" · %s free in the ledger", usdCents(free))
		}
	}
	maxSOL = max(0, maxSOL-0.00001) // leave the network fee
	amount := widget.NewEntry()
	amount.SetPlaceHolder("0.1")
	value := widget.NewLabel("")
	amount.OnChanged = func(s string) {
		l, err := parseSOL(s)
		if err != nil || price == 0 {
			value.SetText("")
			return
		}
		value.SetText(fmt.Sprintf("≈ %s at %s per SOL", usd(float64(l)/wallet.LamportsPerSOL*price), priceUSD(price)))
	}
	maxBtn := widget.NewButton("Max", func() { amount.SetText(strconv.FormatFloat(math.Floor(maxSOL*1e6)/1e6, 'f', -1, 64)) })
	maxBtn.Importance = widget.LowImportance
	amount.Validator = func(s string) error {
		l, err := parseSOL(s)
		if err != nil {
			return err
		}
		if float64(l)/wallet.LamportsPerSOL > maxSOL+1e-9 {
			return fmt.Errorf("at most %.6g SOL", maxSOL)
		}
		return nil
	}
	title, verb := "Fund the bot", "Continue"
	if !toBot {
		title = "Withdraw to your main account"
	}
	items := []*widget.FormItem{
		{Text: "Amount (SOL)", Widget: container.NewBorder(nil, nil, nil, maxBtn, amount),
			HintText: fmt.Sprintf("Available: %.6g SOL%s.", maxSOL, limit)},
		{Text: "", Widget: value},
	}
	d := dialog.NewForm(title, verb, "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		l, _ := parseSOL(amount.Text)
		go func() {
			fee, err := u.be.TransferFee(toBot, l)
			fyne.Do(func() {
				if err != nil {
					showError(u, err)
					return
				}
				u.confirmTransfer(toBot, l, fee, price, src.Address, dst.Address)
			})
		}()
	}, u.win)
	d.Resize(fyne.NewSize(480, 260))
	d.Show()
}

func (u *UI) confirmTransfer(toBot bool, lamports, fee uint64, price float64, from, to string) {
	sol := float64(lamports) / wallet.LamportsPerSOL
	names := [2]string{"Main account", "Bot account"}
	if !toBot {
		names = [2]string{"Bot account", "Main account"}
	}
	msg := fmt.Sprintf("DEVNET · test SOL\n\nSend %.9g SOL (%s)\nfrom %s  %s\nto     %s  %s\n\nNetwork fee: %.9g SOL",
		sol, usd(sol*price), names[0], shortAddress(from), names[1], shortAddress(to), float64(fee)/wallet.LamportsPerSOL)
	if toBot {
		msg += "\n\nThe ledger is credited with this value: the bot may then trade it, entry by entry, after your approval."
	} else {
		msg += "\n\nThis value leaves the bot's ledger first."
	}
	confirm(u, "Confirm the transfer", msg, func() {
		go func() {
			var sig string
			var err error
			if toBot {
				sig, err = u.be.FundBot(lamports)
			} else {
				sig, err = u.be.WithdrawToMain(lamports)
			}
			fyne.Do(func() {
				u.settings = u.be.Settings()
				u.refreshMode()
				if err != nil {
					showError(u, err)
					return
				}
				u.showTransferDone(sig)
			})
		}()
	})
}

func (u *UI) showTransferDone(sig string) {
	link, _ := url.Parse(wallet.ExplorerURL(sig))
	content := container.NewVBox(
		widget.NewLabel("Confirmed on devnet."),
		widget.NewHyperlink("See it on Solana Explorer", link),
	)
	dialog.ShowCustom("Transfer done", "OK", content, u.win)
}

func solPrice(w WalletView) float64 {
	for _, h := range w.Holdings {
		if h.Symbol == "SOL" && h.Mint == "" && h.Priced {
			return h.Price
		}
	}
	return 0
}

func solBalance(w WalletView) float64 {
	for _, h := range w.Holdings {
		if h.Symbol == "SOL" && h.Mint == "" {
			return h.Amount
		}
	}
	return 0
}

// parseSOL reads an amount of SOL ("0.25", "1,5") into lamports, exactly.
func parseSOL(s string) (uint64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if s == "" || len(frac) > 9 || strings.ContainsAny(whole+frac, "+-") {
		return 0, errors.New("an amount of SOL, with at most 9 decimals")
	}
	w, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || w > 1e9 {
		return 0, errors.New("an amount of SOL, with at most 9 decimals")
	}
	f := uint64(0)
	if frac != "" {
		if f, err = strconv.ParseUint(frac+strings.Repeat("0", 9-len(frac)), 10, 64); err != nil {
			return 0, errors.New("an amount of SOL, with at most 9 decimals")
		}
	}
	l := w*wallet.LamportsPerSOL + f
	if l == 0 {
		return 0, errors.New("the amount must be positive")
	}
	return l, nil
}
