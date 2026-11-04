package gui

import (
	"hash/fnv"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Card is a rounded, softly bordered surface.
type Card struct {
	widget.BaseWidget
	content fyne.CanvasObject
}

func newCard(content fyne.CanvasObject) *Card {
	c := &Card{content: content}
	c.ExtendBaseWidget(c)
	return c
}

func (c *Card) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = 18
	bg.StrokeWidth = 1
	inner := container.New(layout.NewCustomPaddedLayout(16, 16, 18, 18), c.content)
	r := &cardRenderer{bg: bg, inner: inner}
	r.Refresh()
	return r
}

type cardRenderer struct {
	bg    *canvas.Rectangle
	inner *fyne.Container
}

func (r *cardRenderer) Layout(s fyne.Size) {
	r.bg.Resize(s)
	r.inner.Resize(s)
}
func (r *cardRenderer) MinSize() fyne.Size           { return r.inner.MinSize() }
func (r *cardRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.inner} }
func (r *cardRenderer) Destroy()                     {}
func (r *cardRenderer) Refresh() {
	r.bg.FillColor = themeColor(colorCard)
	r.bg.StrokeColor = themeColor(colorCardBorder)
	r.bg.Refresh()
	r.inner.Refresh()
}

// Toggle is an animated on/off switch.
type Toggle struct {
	widget.BaseWidget
	On        bool
	OnChanged func(bool)
	pos       float32 // knob position, 0 (off) → 1 (on)
}

func newToggle(on bool, changed func(bool)) *Toggle {
	t := &Toggle{On: on, OnChanged: changed}
	if on {
		t.pos = 1
	}
	t.ExtendBaseWidget(t)
	return t
}

// SetOn changes the state, notifying OnChanged when notify is true.
func (t *Toggle) SetOn(on, notify bool) {
	if on == t.On {
		return
	}
	t.On = on
	from, to := t.pos, float32(0)
	if on {
		to = 1
	}
	anim := fyne.NewAnimation(160*time.Millisecond, func(f float32) {
		t.pos = from + (to-from)*f
		t.Refresh()
	})
	anim.Curve = fyne.AnimationEaseInOut
	anim.Start()
	if notify && t.OnChanged != nil {
		t.OnChanged(on)
	}
}

func (t *Toggle) Tapped(*fyne.PointEvent) { t.SetOn(!t.On, true) }
func (t *Toggle) Cursor() desktop.Cursor  { return desktop.PointerCursor }
func (t *Toggle) MinSize() fyne.Size      { return fyne.NewSize(52, 30) }
func (t *Toggle) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(color.Transparent)
	track.CornerRadius = 15
	knob := canvas.NewCircle(color.White)
	knob.StrokeWidth = 1
	r := &toggleRenderer{t: t, track: track, knob: knob}
	r.Refresh()
	return r
}

type toggleRenderer struct {
	t     *Toggle
	track *canvas.Rectangle
	knob  *canvas.Circle
	size  fyne.Size
}

func (r *toggleRenderer) Layout(s fyne.Size) { r.size = s; r.place() }
func (r *toggleRenderer) MinSize() fyne.Size { return r.t.MinSize() }
func (r *toggleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.knob}
}
func (r *toggleRenderer) Destroy() {}
func (r *toggleRenderer) place() {
	w, h := float32(52), float32(30)
	y := (r.size.Height - h) / 2
	r.track.Move(fyne.NewPos(0, y))
	r.track.Resize(fyne.NewSize(w, h))
	d := h - 6
	x := 3 + (w-6-d)*r.t.pos
	r.knob.Position1 = fyne.NewPos(x, y+3)
	r.knob.Position2 = fyne.NewPos(x+d, y+3+d)
}
func (r *toggleRenderer) Refresh() {
	off := themeColor(theme.ColorNameInputBorder)
	on := themeColor(theme.ColorNamePrimary)
	r.track.FillColor = blend(off, on, r.t.pos)
	r.knob.StrokeColor = withAlpha(themeColor(theme.ColorNameShadow), 0x40)
	r.place()
	r.track.Refresh()
	r.knob.Refresh()
}

func blend(a, b color.Color, f float32) color.NRGBA {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	mix := func(x, y uint32) uint8 { return uint8((float32(x>>8)*(1-f) + float32(y>>8)*f)) }
	return color.NRGBA{R: mix(ar, br), G: mix(ag, bg), B: mix(ab, bb), A: mix(aa, ba)}
}

// coinPalette are warm, distinct colours for coin badges.
var coinPalette = []color.NRGBA{
	hex(0xE0763A), hex(0xD9A441), hex(0x4F9A6A), hex(0xC4605A), hex(0x8E6BB8),
	hex(0x3E8FB0), hex(0xB57A4B), hex(0xD46A8C), hex(0x6A8F3F), hex(0x5C7AA6),
}

// coinBadge is a round badge with the first letters of a ticker.
// coinBadge is the logo of a crypto, or a coloured disc with its ticker
// until the logo is loaded (or when it has none).
func coinBadge(base string, size float32) fyne.CanvasObject {
	if logos != nil && validLogoTicker(base) {
		return logos.badge(base, size, letterBadge(base, size))
	}
	return letterBadge(base, size)
}

func letterBadge(base string, size float32) fyne.CanvasObject {
	h := fnv.New32a()
	h.Write([]byte(base))
	bg := canvas.NewCircle(coinPalette[h.Sum32()%uint32(len(coinPalette))])
	label := base
	if len(label) > 3 {
		label = label[:3]
	}
	txt := canvas.NewText(label, color.White)
	txt.TextStyle.Bold = true
	txt.TextSize = size * 0.34
	txt.Alignment = fyne.TextAlignCenter
	holder := canvas.NewRectangle(color.Transparent)
	holder.SetMinSize(fyne.NewSize(size, size))
	return container.NewStack(holder, bg, container.NewCenter(txt))
}

// text returns a canvas text with the given size and style.
func text(s string, size float32, bold bool, c color.Color) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextSize = size
	t.TextStyle.Bold = bold
	return t
}

// fixedWidth forces the minimum width of o.
func fixedWidth(o fyne.CanvasObject, w float32) fyne.CanvasObject {
	spacer := canvas.NewRectangle(color.Transparent)
	spacer.SetMinSize(fyne.NewSize(w, 0))
	return container.NewStack(spacer, o)
}
