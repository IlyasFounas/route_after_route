package main

import (
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/IlyasFounas/route_after_route/internal/live"
)

// effortLocked is the saved level, or the default one.
func (s *session) effortLocked() live.Effort {
	e := live.Effort(s.settings.Effort)
	if !slices.Contains(live.Efforts, e) {
		return live.DefaultEffort
	}
	return e
}

// SetEffort changes the performance level, live: cores, OS priority, UI
// refresh rate and re-evaluation period. The engine is not restarted. The
// note, when not empty, tells what only applies at the next launch.
func (s *session) SetEffort(e live.Effort) (string, error) {
	if !slices.Contains(live.Efforts, e) {
		return "", fmt.Errorf("unknown effort %q: choose %s", e, live.EffortNames())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Effort = string(e)
	if err := s.saveSettingsLocked(); err != nil {
		return "", err
	}
	note := applyResources(e.Params())
	if s.eng != nil {
		s.eng.SetPace(s.paceLocked())
	}
	return note, nil
}

// paceLocked is the engine pace of the current level. The demo keeps the
// re-evaluation period of its config: its clock replays days per second.
func (s *session) paceLocked() live.Pace {
	p := s.effortLocked().Params()
	pace := live.Pace{SnapshotEvery: p.SnapshotEvery, ReevaluateEvery: p.ReevaluateEvery, BracketStride: p.BracketStride}
	if s.fixed {
		pace.ReevaluateEvery = 0
	}
	return pace
}

var (
	resourcesMu sync.Mutex
	appliedNice int // the nice value set by applyResources (0 at launch)
	// niceFn is setNice; tests replace it so that they never renice the
	// test binary.
	niceFn = setNice
)

// applyResources sets the cores used and the OS priority of the process.
// The priority is best effort: if the system refuses to raise it back, the
// returned note says it will be normal again at the next launch.
func applyResources(p live.EffortParams) string {
	if p.Procs > 0 {
		runtime.GOMAXPROCS(p.Procs)
	} else {
		runtime.SetDefaultGOMAXPROCS()
	}
	resourcesMu.Lock()
	defer resourcesMu.Unlock()
	if p.Nice == appliedNice {
		return ""
	}
	if err := niceFn(p.Nice); err != nil {
		if p.Nice < appliedNice {
			return "The system keeps the lower priority until the next launch: cores and refresh rate are already back."
		}
		return "The system priority could not be changed: " + err.Error()
	}
	appliedNice = p.Nice
	return ""
}
