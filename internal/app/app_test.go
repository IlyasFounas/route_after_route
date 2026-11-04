package app

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/output"
	"github.com/IlyasFounas/route_after_route/internal/profile"

	_ "github.com/IlyasFounas/route_after_route/internal/market/csvfile"
)

var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T) Input {
	t.Helper()
	cfg, err := LoadConfig("../../examples/config.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load("../../examples/profile.json")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := cfg.OpenMarkets()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := cfg.OpenStrategies()
	if err != nil {
		t.Fatal(err)
	}
	return Input{Profile: p, Config: cfg, Markets: ms, Strategies: ss, Now: time.Date(2022, 9, 28, 0, 0, 0, 0, time.UTC)}
}

// TestCycleGolden pins the full report of the example configuration: any
// change in data, scoring, arbitration or output shows up as a diff.
// Regenerate with: go test ./internal/app -update
func TestCycleGolden(t *testing.T) {
	in := load(t)
	res, err := Cycle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := output.WriteJSON(&got, res.Report); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "cycle1.golden.json")
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("report differs from %s; run `go test ./internal/app -update` and review the diff", golden)
	}

	// Determinism: a second identical run gives byte-identical output.
	res2, _ := Cycle(context.Background(), in)
	var again bytes.Buffer
	output.WriteJSON(&again, res2.Report)
	if !bytes.Equal(got.Bytes(), again.Bytes()) {
		t.Fatal("two identical cycles produced different reports")
	}
}

// TestSecondCycleReinjectsState checks the loop: cycle 2 starts from the
// state of cycle 1 and keeps positions whose signal did not change.
func TestSecondCycleReinjectsState(t *testing.T) {
	in := load(t)
	r1, err := Cycle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.State, in.Now = r1.State, in.Now.Add(24*time.Hour)
	r2, err := Cycle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r2.State.Cycle != 2 {
		t.Fatalf("cycle = %d, want 2", r2.State.Cycle)
	}
	for _, o := range r2.Report.Orders {
		if o.Kind != output.OrderHold {
			t.Fatalf("same data and signals: expected only holds, got %+v", o)
		}
	}
	if len(r2.Report.Orders) != len(r1.State.Positions) {
		t.Fatalf("expected one hold per position")
	}
}
