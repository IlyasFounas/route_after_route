package gui

import (
	"image"
	"image/color"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Point is one price observation.
type Point struct {
	T time.Time
	V float64
}

// Marker is a bot order drawn on the curve.
type Marker struct {
	T    time.Time
	V    float64
	Kind string // "buy" or "sell"
}

// LineChart is a minimal price curve: a smooth line with a soft gradient
// fill, green when the price rose over the range, coral when it fell.
// Hovering shows the price at the cursor. Use from the Fyne goroutine only.
type LineChart struct {
	widget.BaseWidget

	points  []Point
	markers []Marker
	entry   float64 // entry price of the bot's open position (0 = none)
	message string  // shown instead of the curve when there is no data

	hover    int // index of the hovered point, -1 = none
	dataGen  int // bumped on every SetData to regenerate the raster
	rendered int
}

func newLineChart() *LineChart {
	c := &LineChart{hover: -1, message: "Loading…"}
	c.ExtendBaseWidget(c)
	return c
}

// SetData replaces the curve.
func (c *LineChart) SetData(points []Point, markers []Marker, entry float64) {
	c.points, c.markers, c.entry, c.message = points, markers, entry, ""
	c.dataGen++
	c.Refresh()
}

// SetMessage clears the curve and shows a message.
func (c *LineChart) SetMessage(m string) {
	c.points, c.markers, c.message = nil, nil, m
	c.dataGen++
	c.Refresh()
}

func (c *LineChart) rising() bool {
	return len(c.points) < 2 || c.points[len(c.points)-1].V >= c.points[0].V
}

func (c *LineChart) lineColor() color.Color {
	if c.rising() {
		return themeColor(colorUp)
	}
	return themeColor(colorDown)
}

// Hover handling.
func (c *LineChart) MouseIn(e *desktop.MouseEvent) { c.MouseMoved(e) }
func (c *LineChart) MouseOut()                     { c.hover = -1; c.Refresh() }
func (c *LineChart) MouseMoved(e *desktop.MouseEvent) {
	w := c.Size().Width - chartRight
	if len(c.points) < 2 || w <= 0 {
		return
	}
	f := float64(e.Position.X / w)
	i := int(math.Round(math.Max(0, math.Min(1, f)) * float64(len(c.points)-1)))
	if i != c.hover {
		c.hover = i
		c.Refresh()
	}
}

const (
	chartRight  = 70 // price labels
	chartTop    = 14
	chartBottom = 26 // time labels
)

func (c *LineChart) CreateRenderer() fyne.WidgetRenderer {
	r := &lineRenderer{c: c}
	r.raster = canvas.NewRaster(r.draw)
	return r
}

type lineRenderer struct {
	c       *LineChart
	raster  *canvas.Raster
	size    fyne.Size
	overlay []fyne.CanvasObject
}

func (r *lineRenderer) Layout(s fyne.Size) {
	r.size = s
	r.raster.Resize(s)
	r.buildOverlay()
}
func (r *lineRenderer) MinSize() fyne.Size { return fyne.NewSize(360, 220) }
func (r *lineRenderer) Objects() []fyne.CanvasObject {
	return append([]fyne.CanvasObject{r.raster}, r.overlay...)
}
func (r *lineRenderer) Destroy() {}
func (r *lineRenderer) Refresh() {
	if r.c.rendered != r.c.dataGen {
		r.c.rendered = r.c.dataGen
		r.raster.Refresh()
	}
	r.buildOverlay()
	canvas.Refresh(r.c)
}

// geometry maps prices and indices to positions (in Fyne units).
type geometry struct {
	lo, hi       float64
	plotW, plotH float32
	n            int
}

func (r *lineRenderer) geometry() (geometry, bool) {
	pts := r.c.points
	g := geometry{plotW: r.size.Width - chartRight, plotH: r.size.Height - chartTop - chartBottom, n: len(pts)}
	if len(pts) < 2 || g.plotW < 20 || g.plotH < 20 {
		return g, false
	}
	g.lo, g.hi = math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		g.lo, g.hi = math.Min(g.lo, p.V), math.Max(g.hi, p.V)
	}
	if e := r.c.entry; e > 0 && e > g.lo*0.9 && e < g.hi*1.1 {
		g.lo, g.hi = math.Min(g.lo, e), math.Max(g.hi, e)
	}
	pad := (g.hi - g.lo) * 0.12
	if pad == 0 {
		pad = math.Max(g.hi*0.01, 1e-9)
	}
	g.lo, g.hi = g.lo-pad, g.hi+pad*0.6
	return g, true
}

func (g geometry) x(i int) float32 { return float32(i) / float32(g.n-1) * g.plotW }
func (g geometry) y(v float64) float32 {
	return chartTop + float32((g.hi-v)/(g.hi-g.lo))*g.plotH
}

// draw renders the area and the line at pixel resolution.
func (r *lineRenderer) draw(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	g, ok := r.geometry()
	if !ok || r.size.Width <= 0 {
		return img
	}
	scale := float64(w) / float64(r.size.Width)
	col := color.NRGBAModel.Convert(r.c.lineColor()).(color.NRGBA)
	pts := r.c.points
	plotPx := int(float64(g.plotW) * scale)
	bottom := float64(chartTop+g.plotH) * scale
	value := func(px float64) float64 {
		f := px / float64(max(plotPx-1, 1)) * float64(len(pts)-1)
		i := int(math.Floor(f))
		if i >= len(pts)-1 {
			return pts[len(pts)-1].V
		}
		frac := f - float64(i)
		return pts[i].V*(1-frac) + pts[i+1].V*frac
	}
	ypx := func(v float64) float64 { return float64(g.y(v)) * scale }
	half := 1.25 * scale // line half-thickness
	plotTop := float64(chartTop) * scale
	for px := 0; px < plotPx && px < w; px++ {
		y0 := ypx(value(float64(px)))
		y1 := ypx(value(float64(px) + 1))
		top, bot := math.Min(y0, y1)-half, math.Max(y0, y1)+half
		// Gradient fill under the curve. The opacity depends on the height in
		// the chart only, so neighbouring columns blend seamlessly.
		for py := int(math.Ceil(y0)); py < int(bottom) && py < h; py++ {
			t := (float64(py) - plotTop) / math.Max(bottom-plotTop, 1)
			a := 0.26 * math.Pow(math.Max(0, 1-t), 1.4)
			if py == int(math.Ceil(y0)) { // anti-alias the top edge of the fill
				a *= math.Ceil(y0) - y0 + 0.5
			}
			img.SetNRGBA(px, py, color.NRGBA{R: col.R, G: col.G, B: col.B, A: uint8(math.Min(a, 1) * 255)})
		}
		// Anti-aliased line: coverage of each pixel by [top, bot].
		for py := int(math.Floor(top)); py <= int(math.Ceil(bot)) && py < h; py++ {
			if py < 0 {
				continue
			}
			cov := math.Min(bot, float64(py+1)) - math.Max(top, float64(py))
			if cov <= 0 {
				continue
			}
			a := uint8(math.Min(1, cov) * 255)
			if old := img.NRGBAAt(px, py); old.A > a {
				a = old.A
			}
			img.SetNRGBA(px, py, color.NRGBA{R: col.R, G: col.G, B: col.B, A: a})
		}
	}
	return img
}

func (r *lineRenderer) buildOverlay() {
	c := r.c
	muted := themeColor(colorMuted)
	r.overlay = r.overlay[:0]
	g, ok := r.geometry()
	if !ok {
		msg := c.message
		if msg == "" {
			msg = "Waiting for prices…"
		}
		t := text(msg, 15, false, muted)
		t.Alignment = fyne.TextAlignCenter
		t.Resize(fyne.NewSize(r.size.Width, 24))
		t.Move(fyne.NewPos(0, r.size.Height/2-12))
		r.overlay = append(r.overlay, t)
		return
	}
	sep := themeColor(theme.ColorNameSeparator)

	// Three faint price guides with labels on the right.
	for k := 0; k < 3; k++ {
		v := g.lo + (g.hi-g.lo)*(0.2+0.3*float64(k))
		y := g.y(v)
		l := canvas.NewLine(sep)
		l.StrokeWidth = 1
		l.Position1, l.Position2 = fyne.NewPos(0, y), fyne.NewPos(g.plotW, y)
		t := text(priceUSD(v), 11, false, muted)
		t.Move(fyne.NewPos(g.plotW+8, y-8))
		r.overlay = append(r.overlay, l, t)
	}

	// Start and end dates.
	pts := c.points
	span := pts[len(pts)-1].T.Sub(pts[0].T)
	layout := "Jan 2 15:04"
	switch {
	case span <= time.Hour:
		layout = "15:04:05"
	case span > 3*24*time.Hour:
		layout = "Jan 2"
	}
	first := text(pts[0].T.Local().Format(layout), 11, false, muted)
	first.Move(fyne.NewPos(0, r.size.Height-chartBottom+8))
	last := text(pts[len(pts)-1].T.Local().Format(layout), 11, false, muted)
	last.Alignment = fyne.TextAlignTrailing
	last.Resize(fyne.NewSize(100, 14))
	last.Move(fyne.NewPos(g.plotW-100, r.size.Height-chartBottom+8))
	r.overlay = append(r.overlay, first, last)

	// Entry price of the bot's position.
	if c.entry > g.lo && c.entry < g.hi {
		l := canvas.NewLine(withAlpha(themeColor(theme.ColorNamePrimary), 0x99))
		l.StrokeWidth = 1.5
		y := g.y(c.entry)
		l.Position1, l.Position2 = fyne.NewPos(0, y), fyne.NewPos(g.plotW, y)
		t := text("bot entry", 11, true, themeColor(theme.ColorNamePrimary))
		t.Move(fyne.NewPos(6, y-17))
		r.overlay = append(r.overlay, l, t)
	}

	// Bot orders.
	for _, m := range c.markers {
		if m.T.Before(pts[0].T) || m.T.After(pts[len(pts)-1].T) {
			continue
		}
		i := indexAt(pts, m.T)
		col := themeColor(colorUp)
		if m.Kind == "sell" {
			col = themeColor(colorDown)
		}
		dot := canvas.NewCircle(col)
		dot.StrokeColor, dot.StrokeWidth = themeColor(colorCard), 2
		x, y := g.x(i), g.y(m.V)
		dot.Position1, dot.Position2 = fyne.NewPos(x-6, y-6), fyne.NewPos(x+6, y+6)
		r.overlay = append(r.overlay, dot)
	}

	// Last price: a dot and a tag.
	lastV := pts[len(pts)-1].V
	lc := c.lineColor()
	ly := g.y(lastV)
	halo := canvas.NewCircle(withAlpha(lc, 0x33))
	halo.Position1, halo.Position2 = fyne.NewPos(g.plotW-9, ly-9), fyne.NewPos(g.plotW+9, ly+9)
	dot := canvas.NewCircle(lc)
	dot.Position1, dot.Position2 = fyne.NewPos(g.plotW-4, ly-4), fyne.NewPos(g.plotW+4, ly+4)
	tag := canvas.NewRectangle(lc)
	tag.CornerRadius = 8
	tag.Move(fyne.NewPos(g.plotW+6, ly-10))
	tag.Resize(fyne.NewSize(chartRight-8, 20))
	tagText := text(priceUSD(lastV), 11, true, color.White)
	tagText.Alignment = fyne.TextAlignCenter
	tagText.Resize(fyne.NewSize(chartRight-8, 20))
	tagText.Move(fyne.NewPos(g.plotW+6, ly-10))
	r.overlay = append(r.overlay, halo, dot, tag, tagText)

	// Hover: guide, dot and tooltip.
	if i := c.hover; i >= 0 && i < len(pts) {
		x, y := g.x(i), g.y(pts[i].V)
		guide := canvas.NewLine(withAlpha(themeColor(theme.ColorNameForeground), 0x33))
		guide.StrokeWidth = 1
		guide.Position1, guide.Position2 = fyne.NewPos(x, chartTop), fyne.NewPos(x, chartTop+g.plotH)
		hd := canvas.NewCircle(lc)
		hd.StrokeColor, hd.StrokeWidth = themeColor(colorCard), 2
		hd.Position1, hd.Position2 = fyne.NewPos(x-5, y-5), fyne.NewPos(x+5, y+5)
		hoverLayout := "Mon Jan 2, 15:04"
		if span <= time.Hour {
			hoverLayout = "15:04:05"
		}
		label := priceUSD(pts[i].V) + "  ·  " + pts[i].T.Local().Format(hoverLayout)
		tt := text(label, 12, true, themeColor(theme.ColorNameForeground))
		tw := tt.MinSize().Width + 20
		tx := min(max(x-tw/2, 0), g.plotW-tw)
		box := canvas.NewRectangle(themeColor(colorCard))
		box.CornerRadius = 10
		box.StrokeColor, box.StrokeWidth = themeColor(colorCardBorder), 1
		box.Move(fyne.NewPos(tx, 0))
		box.Resize(fyne.NewSize(tw, 26))
		tt.Move(fyne.NewPos(tx+10, 5))
		r.overlay = append(r.overlay, guide, hd, box, tt)
	}
}

// indexAt returns the index of the last point at or before t.
func indexAt(pts []Point, t time.Time) int {
	lo, hi := 0, len(pts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if pts[mid].T.After(t) {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	return lo
}
