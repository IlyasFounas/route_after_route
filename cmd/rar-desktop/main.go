// Command rar-desktop is the desktop application: the algorithm watches the
// user's favorite cryptos on 1-second real-time candles, notifies every
// opportunity and only enters a position once the user approves it (devnet
// ledger). It runs behind a minimal window and a system-tray icon (closing
// the window does not stop it).
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	routeafterroute "github.com/IlyasFounas/route_after_route"
	"github.com/IlyasFounas/route_after_route/internal/gui"

	// Market adapters register themselves here.
	_ "github.com/IlyasFounas/route_after_route/internal/market/binance"
	_ "github.com/IlyasFounas/route_after_route/internal/market/csvfile"
)

//go:embed Icon.png
var iconPNG []byte

const appID = "io.github.ilyasfounas.route_after_route"

func main() {
	profilePath := flag.String("profile", "", "profile JSON file (default: <config dir>/profile.json)")
	configPath := flag.String("config", "", "use this config file as is: its markets replace the crypto picker")
	statePath := flag.String("state", "", "ledger file (default: devnet-ledger.json, or state.json next to a --config/--demo config)")
	pair := flag.String("pair", "", "crypto to show (added to the watchlist), e.g. BTCUSDT")
	demo := flag.Bool("demo", false, "offline demo: replay synthetic markets instead of live Binance prices")
	flag.Parse()

	a := fyneapp.NewWithID(appID)
	a.SetIcon(fyne.NewStaticResource("Icon.png", iconPNG))

	p, fixed, err := resolvePaths(*profilePath, *configPath, *statePath, *demo)
	if err != nil {
		fatal(a, err)
		return
	}
	s, err := newSession(p, fixed, *pair)
	if err == nil {
		err = s.start()
	}
	if err != nil {
		fatal(a, err)
		return
	}

	ui := gui.New(a, s)
	// Ctrl+C / SIGTERM: stop the engine (saving the state) and quit.
	sig, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-sig.Done()
		fyne.Do(func() { s.Quit(); a.Quit() })
	}()
	ui.Start()
	ui.Window().Show()
	a.Run()
	s.Quit()
}

// fatal shows a startup error in a window (the app is usually launched by a
// double-click, without a terminal).
func fatal(a fyne.App, err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	a.Settings().SetTheme(gui.WarmTheme{})
	w := a.NewWindow("route_after_route — startup error")
	msg := widget.NewLabel(err.Error())
	msg.Wrapping = fyne.TextWrapWord
	w.SetContent(container.NewBorder(nil, widget.NewButton("Quit", a.Quit), nil, nil, container.NewScroll(msg)))
	w.Resize(fyne.NewSize(640, 280))
	w.ShowAndRun()
}

// resolvePaths returns the user's files, bootstrapping the config directory
// on first launch. fixed is true when the markets come from a config file
// as is (--config or --demo).
func resolvePaths(profilePath, configPath, statePath string, demo bool) (paths, bool, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return paths{}, false, err
	}
	dir := filepath.Join(base, "route_after_route")
	if err := bootstrap(dir); err != nil {
		return paths{}, false, fmt.Errorf("preparing %s: %w", dir, err)
	}
	p := paths{Dir: dir, Profile: profilePath, Config: configPath, State: statePath,
		Settings: filepath.Join(dir, "settings.json"), DevnetKey: filepath.Join(dir, "devnet-wallet.json"),
		MainVault: filepath.Join(dir, "main-wallet.json")}
	fixed := configPath != ""
	if demo {
		p.Config, fixed = filepath.Join(dir, "demo", "config.json"), true
		if statePath == "" {
			p.State = filepath.Join(dir, "demo", "state.json")
		}
	}
	if p.Profile == "" {
		p.Profile = filepath.Join(dir, "profile.json")
	}
	if p.Config == "" {
		p.Config = filepath.Join(dir, "config.json")
	}
	if p.State == "" {
		p.State = filepath.Join(dir, "devnet-ledger.json")
		if fixed {
			p.State = filepath.Join(filepath.Dir(p.Config), "state.json")
		}
	}
	return p, fixed, nil
}

// bootstrap writes the default files that do not exist yet. Existing files
// are never overwritten.
//
//	profile.json       example profile (risk tolerance; its budget is the demo ledger's)
//	config.json        strategies and backtest settings for live Binance prices
//	demo/config.json   offline replay of the synthetic markets (--demo)
//	demo/testdata/     the synthetic markets
func bootstrap(dir string) error {
	// Versions before 0.2 installed the replay demo as config.json: keep it
	// aside so the live template replaces it.
	cfg := filepath.Join(dir, "config.json")
	if raw, err := os.ReadFile(cfg); err == nil && strings.Contains(string(raw), `"replay"`) {
		if err := os.Rename(cfg, filepath.Join(dir, "config.replay-old.json")); err != nil {
			return err
		}
		// Its paper portfolio was on the synthetic markets.
		if err := os.Rename(filepath.Join(dir, "state.json"), filepath.Join(dir, "state.replay-old.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	assets := routeafterroute.Assets
	copies := map[string]string{
		"examples/profile.json":       "profile.json",
		"examples/config.live.json":   "config.json",
		"examples/config.replay.json": "demo/config.json",
	}
	err := fs.WalkDir(assets, "testdata", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			copies[p] = "demo/" + p
		}
		return err
	})
	if err != nil {
		return err
	}
	for src, dst := range copies {
		target := filepath.Join(dir, filepath.FromSlash(dst))
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		raw, err := fs.ReadFile(assets, src)
		if err != nil {
			return err
		}
		if path.Dir(src) == "examples" {
			// Example configs point to ../testdata; in the demo folder the
			// data sits next to the config.
			raw = []byte(strings.ReplaceAll(string(raw), `"../testdata/`, `"testdata/`))
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}
