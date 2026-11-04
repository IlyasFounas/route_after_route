// Command rar runs the route_after_route decision pipeline locally.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/app"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"

	// Market adapters register themselves here, and only here.
	_ "github.com/IlyasFounas/route_after_route/internal/market/binance"
	_ "github.com/IlyasFounas/route_after_route/internal/market/csvfile"
)

const usage = `rar — route_after_route

Usage:
  rar run        --profile P --config C [--state state.json] [--format text|json] [--force] [--dry-run]
  rar backtest   --profile P --config C [--format text|json]
  rar markets    --config C [--profile P]
  rar strategies --config C

  run         one decision cycle: evaluate, arbitrate, print actions/orders, update state
  backtest    evaluate every (market, strategy) pair, without arbitration or state
  markets     list the selected markets and their compatibility with the profile
  strategies  list the configured strategies and their declarations
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	profilePath := fs.String("profile", "", "profile JSON file")
	configPath := fs.String("config", "", "configuration JSON file")
	statePath := fs.String("state", "state.json", "portfolio state file (run)")
	format := fs.String("format", "text", "output format: text or json")
	force := fs.Bool("force", false, "run even if the re-evaluation interval has not elapsed (run)")
	dryRun := fs.Bool("dry-run", false, "do not write the state file (run)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q", *format)
	}
	if *configPath == "" {
		return errors.New("--config is required")
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	loadProfile := func(required bool) (*profile.Profile, error) {
		if *profilePath == "" {
			if required {
				return nil, errors.New("--profile is required")
			}
			return nil, nil
		}
		p, err := profile.Load(*profilePath)
		return &p, err
	}

	switch cmd {
	case "run", "backtest":
		p, err := loadProfile(true)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		state := loop.State{}
		if cmd == "run" {
			if state, err = loop.Load(*statePath); err != nil {
				return err
			}
			if due, next := loop.Due(state, cfg.ReevaluateEvery.Duration, now); !due && !*force {
				fmt.Fprintf(stdout, "Not due yet: next re-evaluation at %s (use --force to run anyway).\n", next.Format(time.RFC3339))
				return nil
			}
		}
		markets, err := cfg.OpenMarkets()
		if err != nil {
			return err
		}
		strats, err := cfg.OpenStrategies()
		if err != nil {
			return err
		}
		res, err := app.Cycle(ctx, app.Input{Profile: *p, Config: cfg, Markets: markets, Strategies: strats, State: state, Now: now})
		if err != nil {
			return err
		}
		switch {
		case *format == "json":
			err = output.WriteJSON(stdout, res.Report)
		case cmd == "backtest":
			err = output.WriteCandidates(stdout, res.Report)
		default:
			err = output.WriteText(stdout, res.Report)
		}
		if err != nil {
			return err
		}
		if cmd == "run" && !*dryRun {
			return loop.Save(*statePath, res.State)
		}
		return nil

	case "markets":
		p, err := loadProfile(false)
		if err != nil {
			return err
		}
		markets, err := cfg.OpenMarkets()
		if err != nil {
			return err
		}
		if *format == "json" {
			ds := make([]market.Descriptor, len(markets))
			for i, m := range markets {
				ds[i] = m.Descriptor()
			}
			return output.WriteJSON(stdout, ds)
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tGRANULARITY\tFEES(bps)\tMIN_CAPITAL\tSHORT\tSYMBOLS\tCOMPATIBILITY")
		for _, m := range markets {
			d := m.Descriptor()
			compat := "-"
			if p != nil {
				compat = "ok"
				if r := market.Compatible(d, *p); len(r) > 0 {
					compat = strings.Join(r, "; ")
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%g\t%s\t%t\t%d\t%s\n", d.ID, d.Kind, d.Granularity, d.Fees.TakerBps, output.Money(d.MinCapital), d.Access.ShortAllowed, len(d.Symbols), compat)
		}
		return tw.Flush()

	case "strategies":
		strats, err := cfg.OpenStrategies()
		if err != nil {
			return err
		}
		type entry struct {
			Name string `json:"name"`
			Spec any    `json:"spec"`
		}
		list := make([]entry, len(strats))
		for i, s := range strats {
			list[i] = entry{Name: s.Name, Spec: s.Spec()}
		}
		return output.WriteJSON(stdout, list)

	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}
