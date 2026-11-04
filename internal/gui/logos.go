package gui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	_ "image/png" // logos are PNG
	"math"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// tickerOf writes a ticker the way it is seen on social networks: $SOL.
func tickerOf(base string) string { return "$" + base }

// logoStore loads the crypto logos once, in the background, and swaps them
// into the badges already on screen. Without a logo (offline, unknown
// asset), the letter badge stays.
type logoStore struct {
	fetch func(ctx context.Context, base string) ([]byte, error)
	sem   chan struct{} // bounds the parallel downloads
	// onUI runs f on the Fyne goroutine (fyne.Do; replaced in tests).
	onUI func(f func())

	mu      sync.Mutex
	imgs    map[string]image.Image // nil: no logo
	waiting map[string][]logoSlot
}

type logoSlot struct {
	c    *fyne.Container
	size float32
}

// logos is the store used by coinBadge (nil: letter badges only, as in tests).
var logos *logoStore

func newLogoStore(fetch func(ctx context.Context, base string) ([]byte, error)) *logoStore {
	return &logoStore{fetch: fetch, sem: make(chan struct{}, 6), onUI: fyne.Do, imgs: map[string]image.Image{}, waiting: map[string][]logoSlot{}}
}

// badge returns a badge showing fallback until the logo of base is loaded.
// It must be called on the Fyne goroutine.
func (s *logoStore) badge(base string, size float32, fallback fyne.CanvasObject) fyne.CanvasObject {
	c := container.NewStack(fallback)
	s.mu.Lock()
	img, known := s.imgs[base]
	if known {
		s.mu.Unlock()
		if img != nil {
			c.Objects = []fyne.CanvasObject{logoImage(img, size)}
		}
		return c
	}
	first := len(s.waiting[base]) == 0
	s.waiting[base] = append(s.waiting[base], logoSlot{c, size})
	s.mu.Unlock()
	if first {
		go s.load(base)
	}
	return c
}

func (s *logoStore) load(base string) {
	s.sem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	raw, err := s.fetch(ctx, base)
	cancel()
	<-s.sem
	var img image.Image
	if err == nil {
		if src, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
			img = roundLogo(src)
		}
	}
	s.mu.Lock()
	s.imgs[base] = img
	slots := s.waiting[base]
	delete(s.waiting, base)
	s.mu.Unlock()
	if img == nil {
		return
	}
	s.onUI(func() {
		for _, sl := range slots {
			sl.c.Objects = []fyne.CanvasObject{logoImage(img, sl.size)}
			sl.c.Refresh()
		}
	})
}

func logoImage(img image.Image, size float32) fyne.CanvasObject {
	ci := canvas.NewImageFromImage(img)
	ci.FillMode = canvas.ImageFillContain
	ci.ScaleMode = canvas.ImageScaleSmooth
	ci.SetMinSize(fyne.NewSize(size, size))
	return ci
}

// roundLogo crops a logo to a disc with an anti-aliased edge, so that every
// logo has the shape of the letter badges (some are square).
func roundLogo(src image.Image) image.Image {
	b := src.Bounds()
	n := min(b.Dx(), b.Dy())
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	draw.Draw(out, out.Bounds(), src, image.Pt(b.Min.X+(b.Dx()-n)/2, b.Min.Y+(b.Dy()-n)/2), draw.Src)
	r := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			d := math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r)
			cover := math.Max(0, math.Min(1, r-d+0.5))
			if cover >= 1 {
				continue
			}
			c := out.NRGBAAt(x, y)
			c.A = uint8(float64(c.A) * cover)
			out.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, c.A})
		}
	}
	return out
}
