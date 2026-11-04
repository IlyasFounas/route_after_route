package gui

import (
	"context"
	"fmt"
	"slices"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// showPicker opens the "follow a crypto" dialog.
func (u *UI) showPicker() {
	var all, shown []Coin
	search := widget.NewEntry()
	search.SetPlaceHolder("Search: $BTC, Solana, PEPE…")
	status := widget.NewLabel("Loading the cryptos available on Binance…")
	status.Importance = widget.LowImportance

	list := widget.NewList(
		func() int { return len(shown) },
		func() fyne.CanvasObject {
			name := widget.NewLabel("")
			name.TextStyle.Bold = true
			sub := widget.NewLabel("")
			sub.Importance = widget.LowImportance
			price := widget.NewLabel("")
			price.Alignment = fyne.TextAlignTrailing
			chg := widget.NewLabel("")
			chg.Alignment = fyne.TextAlignTrailing
			badge := container.NewGridWrap(fyne.NewSize(32, 32), container.NewStack())
			return container.NewBorder(nil, nil, container.NewCenter(badge),
				container.NewHBox(price, fixedWidth(chg, 86)), container.NewHBox(name, sub))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			c := shown[i]
			row := o.(*fyne.Container)
			names := row.Objects[0].(*fyne.Container)
			badge := row.Objects[1].(*fyne.Container).Objects[0].(*fyne.Container).Objects[0].(*fyne.Container)
			right := row.Objects[2].(*fyne.Container)
			badge.Objects = []fyne.CanvasObject{coinBadge(c.Base, 32)}
			names.Objects[0].(*widget.Label).SetText(tickerOf(c.Base))
			names.Objects[1].(*widget.Label).SetText(coinName(c.Base))
			right.Objects[0].(*widget.Label).SetText(priceUSD(c.Price))
			chg := right.Objects[1].(*fyne.Container).Objects[1].(*widget.Label)
			chg.SetText(arrowPct(c.Change))
			chg.Importance = widget.SuccessImportance
			if c.Change < 0 {
				chg.Importance = widget.DangerImportance
			}
			row.Refresh()
		},
	)
	search.OnChanged = func(q string) {
		shown = filterCoins(all, q)
		list.UnselectAll()
		list.Refresh()
		if len(all) > 0 {
			status.SetText(fmt.Sprintf("%d cryptos", len(shown)))
		}
	}

	content := container.NewBorder(container.NewVBox(search, status), nil, nil, nil, list)
	d := dialog.NewCustom("Show or watch a crypto", "Close", content, u.win)
	list.OnSelected = func(i widget.ListItemID) {
		c := shown[i]
		d.Hide()
		u.switchTo(c)
	}
	d.Resize(fyne.NewSize(480, 600))
	d.Show()
	u.win.Canvas().Focus(search)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		coins, err := u.be.Coins(ctx)
		fyne.Do(func() {
			if err != nil {
				status.SetText("Could not load the list: " + err.Error())
				status.Importance = widget.DangerImportance
				status.Refresh()
				return
			}
			all = coins
			search.OnChanged(search.Text)
		})
	}()
}

// switchTo shows c on the chart. A crypto that is not a favorite yet is
// added to the watchlist (the engine restarts to follow it).
func (u *UI) switchTo(c Coin) {
	if c.Pair == u.settings.Pair {
		return
	}
	if slices.Contains(u.settings.Favorites, c.Pair) || u.demo {
		u.showPair(c.Pair)
		return
	}
	if len(u.settings.Favorites) >= MaxFavorites {
		showError(u, fmt.Errorf("you already watch %d cryptos: remove one from the watchlist first", MaxFavorites))
		return
	}
	prev := u.settings.Pair
	u.settings.Pair, u.switching = c.Pair, c.Pair
	u.refreshCoinHeader()
	if u.hasSnap {
		u.apply(u.snap)
	}
	go func() {
		err := u.be.SetPair(c.Pair)
		fyne.Do(func() {
			u.switching = ""
			if err != nil {
				u.settings.Pair = prev
				dialog.ShowError(err, u.win)
			} else {
				u.settings = u.be.Settings()
			}
			u.refreshCoinHeader()
			u.loadHistory()
			if u.hasSnap {
				u.apply(u.snap)
			}
		})
	}()
}
