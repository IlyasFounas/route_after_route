package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/IlyasFounas/route_after_route/internal/live"
)

// currentEffort is the saved performance level.
func (u *UI) currentEffort() live.Effort {
	e := live.Effort(u.settings.Effort)
	return live.Efforts[e.Index()]
}

// setEffort applies a level (off the Fyne goroutine: the backend may block)
// and refreshes the header indicator.
func (u *UI) setEffort(e live.Effort) {
	go func() {
		note, err := u.be.SetEffort(e)
		fyne.Do(func() {
			u.settings = u.be.Settings()
			if err != nil {
				showError(u, err)
				return
			}
			u.settings.Effort = string(e)
			if u.effort != nil {
				u.effort.set(e)
			}
			if note != "" {
				dialog.ShowInformation("Effort: "+string(e), note, u.win)
			}
		})
	}()
}

// showEffort opens the level selector: ←/→ move, Enter applies, Esc
// cancels, like Claude Code's /effort.
func (u *UI) showEffort() {
	desc := widget.NewLabel("")
	desc.Wrapping = fyne.TextWrapWord
	desc.Alignment = fyne.TextAlignCenter
	help := text("Arrow keys to adjust · Enter to apply · Esc to cancel", 11, false, themeColor(colorMuted))
	help.Alignment = fyne.TextAlignCenter
	intro := widget.NewLabel("How much of your computer the algorithm may use. Lower it while you code, watch a movie or play: it keeps every exit on time and only thinks less often.")
	intro.Wrapping = fyne.TextWrapWord

	var d *dialog.CustomDialog
	picker := newEffortPicker(u.currentEffort(), func(e live.Effort) {
		desc.SetText(effortLine(e))
	})
	picker.onSubmit = func(e live.Effort) {
		d.Hide()
		u.setEffort(e)
	}
	picker.onCancel = func() { d.Hide() }
	desc.SetText(effortLine(picker.level))

	apply := widget.NewButton("Apply", func() { picker.onSubmit(picker.level) })
	apply.Importance = widget.HighImportance
	cancel := widget.NewButton("Cancel", picker.onCancel)
	content := container.NewVBox(intro, picker, desc, help)
	d = dialog.NewCustomWithoutButtons("Effort", content, u.win)
	d.SetButtons([]fyne.CanvasObject{cancel, apply})
	d.Resize(fyne.NewSize(520, 0))
	d.Show()
	u.win.Canvas().Focus(picker)
}

func effortLine(e live.Effort) string {
	s := string(e) + " · " + e.Describe()
	if e == live.DefaultEffort {
		s += " · default"
	}
	return s
}

// effortPicker is the five-notch selector: a track, one dot per level, the
// chosen one filled, and the level names under it.
type effortPicker struct {
	widget.BaseWidget
	level    live.Effort
	onChange func(live.Effort)
	onSubmit func(live.Effort)
	onCancel func()
	focused  bool
}

func newEffortPicker(e live.Effort, changed func(live.Effort)) *effortPicker {
	p := &effortPicker{level: e, onChange: changed}
	p.ExtendBaseWidget(p)
	return p
}

func (p *effortPicker) set(i int) {
	i = min(max(i, 0), len(live.Efforts)-1)
	if live.Efforts[i] == p.level {
		return
	}
	p.level = live.Efforts[i]
	p.Refresh()
	if p.onChange != nil {
		p.onChange(p.level)
	}
}

// Tapped picks the notch nearest to the click.
func (p *effortPicker) Tapped(e *fyne.PointEvent) {
	n := len(live.Efforts)
	step := p.Size().Width / float32(n)
	p.set(int(e.Position.X / step))
}

func (p *effortPicker) TypedKey(e *fyne.KeyEvent) {
	switch e.Name {
	case fyne.KeyLeft, fyne.KeyDown:
		p.set(p.level.Index() - 1)
	case fyne.KeyRight, fyne.KeyUp:
		p.set(p.level.Index() + 1)
	case fyne.KeyReturn, fyne.KeyEnter:
		if p.onSubmit != nil {
			p.onSubmit(p.level)
		}
	case fyne.KeyEscape:
		if p.onCancel != nil {
			p.onCancel()
		}
	}
}

func (p *effortPicker) TypedRune(rune)         {}
func (p *effortPicker) FocusGained()           { p.focused = true; p.Refresh() }
func (p *effortPicker) FocusLost()             { p.focused = false; p.Refresh() }
func (p *effortPicker) Cursor() desktop.Cursor { return desktop.PointerCursor }
func (p *effortPicker) MinSize() fyne.Size     { return fyne.NewSize(440, 64) }

func (p *effortPicker) CreateRenderer() fyne.WidgetRenderer {
	r := &pickerRenderer{p: p, track: canvas.NewRectangle(color.Transparent), fill: canvas.NewRectangle(color.Transparent)}
	for _, e := range live.Efforts {
		dot := canvas.NewCircle(color.Transparent)
		dot.StrokeWidth = 2
		r.dots = append(r.dots, dot)
		r.labels = append(r.labels, text(string(e), 12, false, color.Black))
	}
	r.Refresh()
	return r
}

type pickerRenderer struct {
	p           *effortPicker
	track, fill *canvas.Rectangle
	dots        []*canvas.Circle
	labels      []*canvas.Text
	size        fyne.Size
}

const pickerDot = 16

func (r *pickerRenderer) Layout(s fyne.Size) { r.size = s; r.place() }
func (r *pickerRenderer) MinSize() fyne.Size { return r.p.MinSize() }
func (r *pickerRenderer) Destroy()           {}
func (r *pickerRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.track, r.fill}
	for i := range r.dots {
		objs = append(objs, r.dots[i], r.labels[i])
	}
	return objs
}

// centre is the x of notch i.
func (r *pickerRenderer) centre(i int) float32 {
	step := r.size.Width / float32(len(r.dots))
	return step*float32(i) + step/2
}

func (r *pickerRenderer) place() {
	y := float32(16)
	first, last := r.centre(0), r.centre(len(r.dots)-1)
	r.track.Move(fyne.NewPos(first, y-1.5))
	r.track.Resize(fyne.NewSize(last-first, 3))
	cur := r.p.level.Index()
	r.fill.Move(fyne.NewPos(first, y-1.5))
	r.fill.Resize(fyne.NewSize(r.centre(cur)-first, 3))
	for i, d := range r.dots {
		size := float32(pickerDot)
		if i == cur {
			size = pickerDot + 6
		}
		x := r.centre(i)
		d.Position1 = fyne.NewPos(x-size/2, y-size/2)
		d.Position2 = fyne.NewPos(x+size/2, y+size/2)
		l := r.labels[i]
		ls := l.MinSize()
		l.Move(fyne.NewPos(x-ls.Width/2, y+18))
		l.Resize(ls)
	}
}

func (r *pickerRenderer) Refresh() {
	primary := themeColor(theme.ColorNamePrimary)
	muted := themeColor(colorMuted)
	border := themeColor(theme.ColorNameInputBorder)
	bg := themeColor(theme.ColorNameBackground)
	cur := r.p.level.Index()
	r.track.FillColor = border
	r.fill.FillColor = primary
	for i, d := range r.dots {
		switch {
		case i == cur:
			d.FillColor, d.StrokeColor = primary, primary
			if r.p.focused {
				d.StrokeColor = withAlpha(primary, 0x60)
				d.StrokeWidth = 4
			} else {
				d.StrokeWidth = 2
			}
		case i < cur:
			d.FillColor, d.StrokeColor, d.StrokeWidth = bg, primary, 2
		default:
			d.FillColor, d.StrokeColor, d.StrokeWidth = bg, border, 2
		}
		l := r.labels[i]
		l.Color, l.TextStyle.Bold = muted, i == cur
		if i == cur {
			l.Color = themeColor(theme.ColorNameForeground)
		}
	}
	r.place()
	for _, o := range r.Objects() {
		o.Refresh()
	}
}

// effortChip is the header indicator: a five-bar meter and the level name.
// A click opens the selector.
type effortChip struct {
	widget.BaseWidget
	level live.Effort
	tap   func()
}

func newEffortChip(e live.Effort, tap func()) *effortChip {
	c := &effortChip{level: e, tap: tap}
	c.ExtendBaseWidget(c)
	return c
}

func (c *effortChip) set(e live.Effort) {
	c.level = e
	c.Refresh()
}

func (c *effortChip) Tapped(*fyne.PointEvent) {
	if c.tap != nil {
		c.tap()
	}
}
func (c *effortChip) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (c *effortChip) CreateRenderer() fyne.WidgetRenderer {
	r := &chipRenderer{c: c, label: text("", 12, false, color.Black)}
	for range live.Efforts {
		r.bars = append(r.bars, canvas.NewRectangle(color.Transparent))
	}
	r.Refresh()
	return r
}

type chipRenderer struct {
	c     *effortChip
	bars  []*canvas.Rectangle
	label *canvas.Text
	size  fyne.Size
}

const (
	chipBarW   = 3
	chipBarGap = 2
	chipBarH   = 14
)

func (r *chipRenderer) meterWidth() float32 {
	return float32(len(r.bars))*(chipBarW+chipBarGap) - chipBarGap
}

func (r *chipRenderer) MinSize() fyne.Size {
	l := r.label.MinSize()
	return fyne.NewSize(r.meterWidth()+6+l.Width+8, max(l.Height, chipBarH)+8)
}
func (r *chipRenderer) Destroy() {}
func (r *chipRenderer) Objects() []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(r.bars)+1)
	for _, b := range r.bars {
		objs = append(objs, b)
	}
	return append(objs, r.label)
}

func (r *chipRenderer) Layout(s fyne.Size) {
	r.size = s
	base := (s.Height + chipBarH) / 2
	for i, b := range r.bars {
		h := chipBarH * float32(i+1) / float32(len(r.bars))
		b.Move(fyne.NewPos(4+float32(i)*(chipBarW+chipBarGap), base-h))
		b.Resize(fyne.NewSize(chipBarW, h))
	}
	l := r.label.MinSize()
	r.label.Move(fyne.NewPos(4+r.meterWidth()+6, (s.Height-l.Height)/2))
	r.label.Resize(l)
}

func (r *chipRenderer) Refresh() {
	on, off := themeColor(theme.ColorNamePrimary), themeColor(theme.ColorNameInputBorder)
	cur := r.c.level.Index()
	for i, b := range r.bars {
		b.FillColor = off
		if i <= cur {
			b.FillColor = on
		}
		b.CornerRadius = 1
		b.Refresh()
	}
	r.label.Text, r.label.Color = string(live.Efforts[cur]), themeColor(colorMuted)
	r.label.Refresh()
	r.Layout(r.size)
}
