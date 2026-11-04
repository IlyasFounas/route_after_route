package main

import (
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/IlyasFounas/route_after_route/internal/gui"
)

type darkWarm struct{ gui.WarmTheme }

func (d darkWarm) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return d.WarmTheme.Color(n, theme.VariantDark)
}

// TestScreenshot regenerates the README screenshots from a live session:
//
//	SCREENSHOT_DIR=$PWD/docs go test -run TestScreenshot ./cmd/rar-desktop
func TestScreenshot(t *testing.T) {
	out := os.Getenv("SCREENSHOT_DIR")
	if out == "" {
		t.Skip()
	}
	dir := t.TempDir()
	if err := bootstrap(dir); err != nil {
		t.Fatal(err)
	}
	p := paths{Dir: dir, Profile: filepath.Join(dir, "profile.json"), Config: filepath.Join(dir, "config.json"), State: filepath.Join(dir, "state.json"), Settings: filepath.Join(dir, "settings.json")}
	s, err := newSession(p, false, os.Getenv("PAIR"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	defer s.Quit()
	a := test.NewTempApp(t)
	ui := gui.New(a, s)
	ui.Start()
	w := ui.Window()
	w.Resize(fyne.NewSize(1180, 760))
	capture := func(name string) {
		f, err := os.Create(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, w.Canvas().Capture()); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	capture("screenshot-home.png")
	ui.Open("trading")
	time.Sleep(12 * time.Second)
	for name, th := range map[string]fyne.Theme{"screenshot.png": gui.WarmTheme{}, "screenshot-dark.png": darkWarm{}} {
		a.Settings().SetTheme(th)
		time.Sleep(1500 * time.Millisecond)
		capture(name)
	}
}
