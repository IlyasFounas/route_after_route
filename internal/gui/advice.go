package gui

import (
	"fmt"
	"slices"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/live"
	"github.com/IlyasFounas/route_after_route/internal/loop"
	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/output"
)

// Tone colours the advice.
type Tone int

const (
	ToneNeutral Tone = iota
	ToneUp
	ToneDown
)

// Advice is the algorithm's current opinion in plain words.
type Advice struct {
	Word     string // Buy, Hold, Sell, Wait
	Line     string // one sentence of explanation
	Tone     Tone
	Strategy string // strategy in charge ("" when none)
}

// keyFor finds the series of pair in the snapshot.
func keyFor(s live.Snapshot, pair string) (loop.PriceKey, bool) {
	for k := range s.Series {
		if k.Symbol == pair {
			return k, true
		}
	}
	return loop.PriceKey{}, false
}

// signalFor returns the action and signal covering pair, if any.
func signalFor(s live.Snapshot, pair string) (output.Action, output.SymbolSignal, bool) {
	for _, a := range s.Report.Actions {
		for _, sig := range a.Signals {
			if sig.Symbol == pair {
				return a, sig, true
			}
		}
	}
	return output.Action{}, output.SymbolSignal{}, false
}

// botPosition returns the bot's open position on pair, if any.
func botPosition(s live.Snapshot, pair string) (loop.Position, bool) {
	for _, p := range s.State.Positions {
		if p.Symbol == pair {
			return p, true
		}
	}
	return loop.Position{}, false
}

// adviceFor turns the snapshot into a plain-language advice for pair.
func adviceFor(s live.Snapshot, pair string) Advice {
	base := baseOf(pair)
	pos, held := botPosition(s, pair)
	a, sig, ok := signalFor(s, pair)
	if !ok {
		if held {
			return Advice{Word: "Sell", Tone: ToneDown,
				Line: fmt.Sprintf("The strategy that bought %s is no longer reliable enough.", base)}
		}
		return Advice{Word: "Wait", Tone: ToneNeutral,
			Line: fmt.Sprintf("No strategy is reliable enough on %s right now, so the bot stays out.", base)}
	}
	adv := Advice{Strategy: a.Strategy}
	switch {
	case sig.Direction == output.Long && held && pos.Direction > 0:
		adv.Word, adv.Tone = "Hold", ToneUp
		adv.Line = fmt.Sprintf("The rise signal on %s is still valid.", base)
	case sig.Direction == output.Long:
		adv.Word, adv.Tone = "Buy", ToneUp
		adv.Line = fmt.Sprintf("The algorithm sees an opportunity on %s (%.0f%% of its budget).", base, sig.Exposure*100)
	case held:
		adv.Word, adv.Tone = "Sell", ToneDown
		adv.Line = fmt.Sprintf("The signal on %s has faded.", base)
	default:
		adv.Word, adv.Tone = "Wait", ToneNeutral
		adv.Line = fmt.Sprintf("No signal on %s for now; the algorithm keeps watching.", base)
	}
	return adv
}

// Ranges are the chart periods, shortest first. The live ones are drawn from
// the engine's 1-second candles; the long ones from coarser candles fetched
// for display (Backend.History).
var Ranges = []string{"1min", "5min", "1h", "1D", "1W", "1M"}

// DefaultRange is the chart period shown at first launch.
const DefaultRange = "5min"

var rangeInfo = map[string]struct {
	d     time.Duration
	label string // button
	span  string // "past …"
}{
	"1min": {time.Minute, "1 min", "past minute"},
	"5min": {5 * time.Minute, "5 min", "past 5 minutes"},
	"1h":   {time.Hour, "1 h", "past hour"},
	"1D":   {24 * time.Hour, "1 D", "past 24 hours"},
	"1W":   {7 * 24 * time.Hour, "1 W", "past week"},
	"1M":   {30 * 24 * time.Hour, "1 M", "past month"},
}

// ValidRange reports whether r is a chart range.
func ValidRange(r string) bool { _, ok := rangeInfo[r]; return ok }

// liveRange reports whether r is drawn from the engine's live series.
func liveRange(r string) bool { return rangeInfo[r].d <= time.Hour }

// rangeDuration maps a chart range to its duration.
func rangeDuration(r string) time.Duration {
	if i, ok := rangeInfo[r]; ok {
		return i.d
	}
	return rangeInfo[DefaultRange].d
}

// pointsFor returns the price curve of pair over the range, and the change
// over that range.
func pointsFor(s live.Snapshot, pair, rng string) ([]Point, float64) {
	k, ok := keyFor(s, pair)
	if !ok {
		return nil, 0
	}
	bars := s.Series[k]
	if len(bars) == 0 {
		return nil, 0
	}
	since := bars[len(bars)-1].Time.Add(-rangeDuration(rng))
	var pts []Point
	for _, b := range bars {
		if !b.Time.Before(since) {
			pts = append(pts, Point{T: b.Time, V: b.Close})
		}
	}
	return pts, change(pts)
}

func change(pts []Point) float64 {
	if len(pts) < 2 || pts[0].V == 0 {
		return 0
	}
	return pts[len(pts)-1].V/pts[0].V - 1
}

// withLivePrice appends the latest price of pair to a long-range curve, so
// that it ends now.
func withLivePrice(pts []Point, s live.Snapshot, pair string) []Point {
	k, ok := keyFor(s, pair)
	if !ok || len(pts) == 0 {
		return pts
	}
	bars := s.Series[k]
	if len(bars) == 0 {
		return pts
	}
	last := bars[len(bars)-1]
	if !last.Time.After(pts[len(pts)-1].T) {
		return pts
	}
	return append(slices.Clip(pts), Point{T: last.Time, V: s.Prices[k]})
}

// opportunityFor returns the pending opportunity on pair, if any.
func opportunityFor(s live.Snapshot, pair string) (live.Opportunity, bool) {
	for _, o := range s.Opportunities {
		if o.Symbol == pair {
			return o, true
		}
	}
	return live.Opportunity{}, false
}

// markersFor returns the executed bot orders on pair.
func markersFor(s live.Snapshot, pair string) []Marker {
	var ms []Marker
	for _, ev := range s.Log {
		if o := ev.Order; ev.Kind == live.EventOrder && o != nil && o.Symbol == pair {
			kind := "buy"
			if o.Kind == output.OrderClose {
				kind = "sell"
			}
			ms = append(ms, Marker{T: ev.Time, V: o.Price, Kind: kind})
		}
	}
	return ms
}

// feedState summarises the market feed status.
func feedState(s live.Snapshot) (string, Tone) {
	if len(s.Statuses) == 0 {
		return "Connecting…", ToneNeutral
	}
	finished := 0
	for _, st := range s.Statuses {
		switch st.State {
		case market.StreamReconnecting, market.StreamConnecting:
			return "Reconnecting…", ToneDown
		case market.StreamFinished:
			finished++
		}
	}
	if finished == len(s.Statuses) {
		return "Replay finished", ToneNeutral
	}
	return "Live", ToneUp
}
