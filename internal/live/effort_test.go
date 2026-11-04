package live

import (
	"testing"
	"time"
)

func TestParseEffort(t *testing.T) {
	for in, want := range map[string]Effort{"low": EffortLow, " HIGH ": EffortHigh, "x": EffortXHigh, "me": EffortMedium, "max": EffortMax, "l": EffortLow} {
		got, err := ParseEffort(in)
		if err != nil || got != want {
			t.Errorf("ParseEffort(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "m", "turbo", "hi gh"} { // "m": medium or max
		if _, err := ParseEffort(in); err == nil {
			t.Errorf("ParseEffort(%q) accepted", in)
		}
	}
}

func TestEffortParamsAreMonotonic(t *testing.T) {
	const cpus = 8
	for i := 1; i < len(Efforts); i++ {
		lo, hi := Efforts[i-1].paramsFor(cpus), Efforts[i].paramsFor(cpus)
		procs := func(p EffortParams) int {
			if p.Procs == 0 {
				return cpus
			}
			return p.Procs
		}
		if procs(hi) < procs(lo) || hi.SnapshotEvery > lo.SnapshotEvery || hi.ReevaluateEvery > lo.ReevaluateEvery ||
			hi.BracketStride > lo.BracketStride || hi.Nice > lo.Nice {
			t.Errorf("%s uses less than %s: %+v vs %+v", Efforts[i], Efforts[i-1], hi, lo)
		}
	}
	if p := EffortHigh.paramsFor(1); p.Procs != 1 {
		t.Errorf("high on one core: Procs = %d", p.Procs)
	}
	if Effort("bogus").Params() != DefaultEffort.Params() || Effort("bogus").Index() != DefaultEffort.Index() {
		t.Error("an unknown level must behave as the default")
	}
	if d := EffortLow.Describe(); d != "re-evaluates every 30 min · UI 1×/s · 1 core · lower priority" {
		t.Errorf("Describe = %q", d)
	}
}

func TestSetPaceKeepsTheTimeAlreadyWaited(t *testing.T) {
	e, _ := newTestEngine(t)
	e.reevalEvery = 30 * time.Minute
	e.nextReeval = t0.Add(30 * time.Minute) // last evaluation at t0
	e.setPace(Pace{ReevaluateEvery: 5 * time.Minute, SnapshotEvery: time.Second})
	if e.nextReeval != t0.Add(5*time.Minute) || e.reevalEvery != 5*time.Minute {
		t.Errorf("next = %s, every = %s", e.nextReeval, e.reevalEvery)
	}
	if e.snapEvery != time.Second || !e.paceChanged {
		t.Errorf("snapshot pace not applied: %s %v", e.snapEvery, e.paceChanged)
	}
	e.paceChanged = false
	e.setPace(Pace{}) // zero fields keep the current values
	if e.reevalEvery != 5*time.Minute || e.snapEvery != time.Second || e.paceChanged {
		t.Error("an empty pace changed something")
	}
}
