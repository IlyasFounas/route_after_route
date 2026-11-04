package live

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Effort is how much of the machine the engine may use, from low (runs
// quietly next to a game or a movie) to max. It never changes what the
// algorithm decides on a candle, nor when exits fire: only how often the
// costly work runs and how many cores it gets.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// DefaultEffort is the level of a fresh install.
const DefaultEffort = EffortHigh

// Efforts lists the levels from the lightest to the heaviest.
var Efforts = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// EffortParams is what a level sets.
type EffortParams struct {
	// Procs caps the cores the process uses (GOMAXPROCS). 0 = all of them.
	Procs int
	// SnapshotEvery is how often the UI is refreshed.
	SnapshotEvery time.Duration
	// ReevaluateEvery is how often the full walk-forward evaluation runs.
	ReevaluateEvery time.Duration
	// BracketStride samples one past window out of BracketStride when the
	// gains and losses of an entry are estimated.
	BracketStride int
	// Nice is the OS priority offset: 0 is normal, higher is gentler.
	Nice int
}

// ParseEffort reads a level name; case and spaces do not matter, and a
// unique prefix is enough ("x" is xhigh, "l" is low).
func ParseEffort(s string) (Effort, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	var found []Effort
	for _, e := range Efforts {
		if string(e) == in {
			return e, nil
		}
		if in != "" && strings.HasPrefix(string(e), in) {
			found = append(found, e)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return "", fmt.Errorf("unknown effort %q: choose %s", s, EffortNames())
}

// EffortNames is "low · medium · high · xhigh · max".
func EffortNames() string {
	names := make([]string, len(Efforts))
	for i, e := range Efforts {
		names[i] = string(e)
	}
	return strings.Join(names, " · ")
}

// Index is the position of e in Efforts (that of DefaultEffort if unknown).
func (e Effort) Index() int {
	for i, x := range Efforts {
		if x == e {
			return i
		}
	}
	return e.orDefault().Index()
}

func (e Effort) orDefault() Effort {
	for _, x := range Efforts {
		if x == e {
			return e
		}
	}
	return DefaultEffort
}

// Params returns the settings of the level, for this machine's cores. An
// unknown level gives those of DefaultEffort.
func (e Effort) Params() EffortParams {
	return e.paramsFor(runtime.NumCPU())
}

func (e Effort) paramsFor(cpus int) EffortParams {
	switch e.orDefault() {
	case EffortLow:
		return EffortParams{Procs: 1, SnapshotEvery: time.Second, ReevaluateEvery: 30 * time.Minute, BracketStride: 8, Nice: 10}
	case EffortMedium:
		return EffortParams{Procs: min(2, cpus), SnapshotEvery: 500 * time.Millisecond, ReevaluateEvery: 15 * time.Minute, BracketStride: 4, Nice: 5}
	case EffortHigh:
		return EffortParams{Procs: min(max(2, cpus/2), cpus), SnapshotEvery: 250 * time.Millisecond, ReevaluateEvery: 5 * time.Minute, BracketStride: 2}
	case EffortXHigh:
		return EffortParams{SnapshotEvery: 250 * time.Millisecond, ReevaluateEvery: 2 * time.Minute, BracketStride: 1}
	default: // max
		return EffortParams{SnapshotEvery: 125 * time.Millisecond, ReevaluateEvery: time.Minute, BracketStride: 1}
	}
}

// Describe says in one line what the level costs and gives.
func (e Effort) Describe() string {
	p := e.Params()
	cores := "all cores"
	if p.Procs > 0 {
		cores = fmt.Sprintf("%d %s", p.Procs, plural(p.Procs, "core", "cores"))
	}
	prio := "normal priority"
	if p.Nice > 0 {
		prio = "lower priority"
	}
	return fmt.Sprintf("re-evaluates every %s · UI %d×/s · %s · %s",
		shortDuration(p.ReevaluateEvery), int(time.Second/p.SnapshotEvery), cores, prio)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}
