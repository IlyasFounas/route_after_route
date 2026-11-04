package gui

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// screen is what the window shows.
type screen int

const (
	screenHome    screen = iota // the prompt
	screenTrading               // the algorithm, live, on every watched crypto
	screenHistory               // the closed trades
)

// Command is a word typed at the home prompt.
type Command struct {
	Name    string
	Aliases []string
	Help    string
	open    func(u *UI)
}

// Commands are the home prompt's words, in the order they are suggested.
// A new screen only needs an entry here.
func Commands() []Command {
	return []Command{
		{Name: "trading", Aliases: []string{"trade", "algo"}, Help: "the algorithm, live, on all your cryptos",
			open: func(u *UI) { u.show(screenTrading) }},
		{Name: "historic", Aliases: []string{"history"}, Help: "every closed trade, gained or lost",
			open: func(u *UI) { u.show(screenHistory) }},
	}
}

// MatchCommand finds the command typed: a name or an alias, or the start of
// one that only a single command has. Case and spaces do not matter.
func MatchCommand(input string) (Command, bool) {
	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return Command{}, false
	}
	var found []Command
	for _, c := range Commands() {
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			if w == in {
				return c, true
			}
			if strings.HasPrefix(w, in) {
				found = append(found, c)
				break
			}
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Command{}, false
}

func commandNames() string {
	var names []string
	for _, c := range Commands() {
		names = append(names, c.Name)
	}
	return strings.Join(names, " · ")
}

// homeIntro explains the app in a few lines, under the title.
var homeIntro = []string{
	"Watches your favorite cryptos second by second and spots short trades of a few tens of seconds.",
	"It never trades on its own: every entry is notified and waits for your approval. Exits are automatic.",
	"Devnet only for now: test SOL, valued at real prices. Everything runs on your machine.",
}

// buildHome is the home screen: the name, what the app does, one prompt.
func (u *UI) buildHome() fyne.CanvasObject {
	muted := themeColor(colorMuted)
	title := text("route_after_route", 46, true, themeColor(theme.ColorNameForeground))
	title.Alignment = fyne.TextAlignCenter
	u.fgTexts = append(u.fgTexts, title)
	intro := container.NewVBox()
	for _, l := range homeIntro {
		t := text(l, 13, false, muted)
		t.Alignment = fyne.TextAlignCenter
		u.mutedTexts = append(u.mutedTexts, t)
		intro.Add(t)
	}
	caret := text(">", 20, true, themeColor(theme.ColorNamePrimary))
	u.prompt = widget.NewEntry()
	u.prompt.TextStyle.Monospace = true
	u.prompt.SetPlaceHolder("trading or historic")
	u.prompt.OnChanged = u.onPromptChanged
	u.prompt.OnSubmitted = func(s string) { u.Open(s) }
	u.promptHint = text(commandNames(), 12, false, muted)
	u.promptHint.Alignment = fyne.TextAlignCenter
	row := container.NewBorder(nil, nil, container.NewCenter(caret), nil, u.prompt)
	gap := func(h float32) fyne.CanvasObject {
		r := canvas.NewRectangle(color.Transparent)
		r.SetMinSize(fyne.NewSize(0, h))
		return r
	}
	return container.NewCenter(container.NewVBox(
		title, gap(4), intro, gap(28),
		container.NewCenter(fixedWidth(row, 460)), u.promptHint))
}

// onPromptChanged says what the typed word opens.
func (u *UI) onPromptChanged(s string) {
	hint, col := commandNames(), themeColor(colorMuted)
	if strings.TrimSpace(s) != "" {
		if c, ok := MatchCommand(s); ok {
			hint, col = c.Name+" — "+c.Help, themeColor(theme.ColorNameForeground)
		} else {
			hint = "no such command · " + commandNames()
		}
	}
	u.setHint(hint, col)
}

func (u *UI) setHint(s string, c color.Color) {
	u.promptHint.Text, u.promptHint.Color = s, c
	u.promptHint.Refresh()
}

// Open runs a home prompt command. It reports whether the input was one.
func (u *UI) Open(input string) bool {
	c, ok := MatchCommand(input)
	if !ok {
		if strings.TrimSpace(input) != "" {
			u.setHint(fmt.Sprintf("“%s” is not a command. Try %s.", strings.TrimSpace(input), strings.ReplaceAll(commandNames(), " · ", " or ")),
				themeColor(colorDown))
		}
		return false
	}
	c.open(u)
	return true
}

// show switches the window to sc. The engine keeps running whatever the
// screen, and opportunities are still notified.
func (u *UI) show(sc screen) {
	u.screen = sc
	for i, o := range u.screens {
		if screen(i) == sc {
			o.Show()
		} else {
			o.Hide()
		}
	}
	switch sc {
	case screenHome:
		u.prompt.SetText("")
		u.onPromptChanged("")
		u.win.Canvas().Focus(u.prompt)
	case screenHistory:
		u.histView.apply(u.snap)
		u.win.Canvas().Unfocus()
	default:
		u.win.Canvas().Unfocus()
	}
}

// homeButton goes back to the prompt (Escape does too).
func (u *UI) homeButton() *widget.Button {
	b := widget.NewButtonWithIcon("", theme.HomeIcon(), func() { u.show(screenHome) })
	b.Importance = widget.LowImportance
	return b
}

// onKey handles the window's shortcuts: Escape goes home.
func (u *UI) onKey(e *fyne.KeyEvent) {
	if e.Name == fyne.KeyEscape && u.screen != screenHome {
		u.show(screenHome)
	}
}
