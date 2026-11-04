package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/gui"
	"github.com/IlyasFounas/route_after_route/internal/live"
)

// stubNice replaces the OS call for the test; it fails when raising the
// priority back, like Linux without privileges.
func stubNice(t *testing.T) *[]int {
	t.Helper()
	var calls []int
	prevFn, prevNice := niceFn, appliedNice
	niceFn = func(n int) error {
		if n < appliedNice {
			return errors.New("permission denied")
		}
		calls = append(calls, n)
		return nil
	}
	t.Cleanup(func() {
		niceFn, appliedNice = prevFn, prevNice
		runtime.SetDefaultGOMAXPROCS()
	})
	return &calls
}

func TestSetEffort(t *testing.T) {
	calls := stubNice(t)
	dir := t.TempDir()
	s := &session{paths: paths{Settings: filepath.Join(dir, "settings.json")}, settings: defaultSettings()}
	if s.effortLocked() != live.DefaultEffort {
		t.Fatalf("fresh install: %q", s.effortLocked())
	}
	if _, err := s.SetEffort("turbo"); err == nil {
		t.Fatal("an unknown level must be refused")
	}
	if note, err := s.SetEffort(live.EffortLow); err != nil || note != "" {
		t.Fatalf("low: %q %v", note, err)
	}
	if runtime.GOMAXPROCS(0) != 1 || len(*calls) != 1 || (*calls)[0] != 10 {
		t.Fatalf("low: GOMAXPROCS %d, nice calls %v", runtime.GOMAXPROCS(0), *calls)
	}
	st, err := loadSettings(s.paths.Settings)
	if err != nil || st.Effort != "low" {
		t.Fatalf("saved effort %q, %v", st.Effort, err)
	}
	// Back to max: the cores come back at once, the priority only at the
	// next launch (the stub refuses, as Linux does).
	note, err := s.SetEffort(live.EffortMax)
	if err != nil || note == "" {
		t.Fatalf("max: note %q, %v", note, err)
	}
	if runtime.GOMAXPROCS(0) != runtime.NumCPU() {
		t.Fatalf("max: GOMAXPROCS %d", runtime.GOMAXPROCS(0))
	}
}

func TestDemoKeepsItsReevaluationPeriod(t *testing.T) {
	s := &session{fixed: true, settings: gui.Settings{Effort: "max"}}
	if p := s.paceLocked(); p.ReevaluateEvery != 0 || p.SnapshotEvery != 125*time.Millisecond {
		t.Fatalf("demo pace %+v", p)
	}
	s.fixed = false
	if p := s.paceLocked(); p.ReevaluateEvery != time.Minute {
		t.Fatalf("live pace %+v", p)
	}
	if st := sanitize(gui.Settings{Effort: "bogus"}); st.Effort != string(live.DefaultEffort) {
		t.Fatalf("sanitize kept %q", st.Effort)
	}
}
