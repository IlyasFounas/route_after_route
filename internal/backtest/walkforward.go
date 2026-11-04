package backtest

import (
	"errors"
	"fmt"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/strategy"
)

// WalkForward splits history into rolling folds: Train bars (warm-up and,
// later, parameter fitting) followed by Test out-of-sample bars. Folds
// advance by Test bars, so test segments never overlap.
type WalkForward struct {
	Train int `json:"train_bars"`
	Test  int `json:"test_bars"`
}

func (w WalkForward) Validate() error {
	if w.Train < 1 || w.Test < 2 {
		return errors.New("walk-forward: need train_bars >= 1 and test_bars >= 2")
	}
	return nil
}

// Builder produces the strategy for a fold from its training segment only.
type Builder func(train []market.Bar) (strategy.Strategy, error)

// Fixed returns a Builder that ignores training data (no fitting).
func Fixed(s strategy.Strategy) Builder {
	return func([]market.Bar) (strategy.Strategy, error) { return s, nil }
}

// Fold is the out-of-sample result of one walk-forward window.
type Fold struct {
	Index     int       `json:"index"`
	TrainFrom time.Time `json:"train_from"`
	TestFrom  time.Time `json:"test_from"`
	TestTo    time.Time `json:"test_to"`
	Result    Result    `json:"-"`
}

var ErrInsufficientData = errors.New("insufficient data for one walk-forward fold")

// RunWalkForward runs every fold. Returns only cover test segments.
func RunWalkForward(bars []market.Bar, build Builder, cfg Config, wf WalkForward) ([]Fold, error) {
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	var folds []Fold
	for k := 0; ; k++ {
		trainStart := k * wf.Test
		trainEnd := trainStart + wf.Train
		testEnd := min(trainEnd+wf.Test, len(bars))
		if testEnd-trainEnd < 2 {
			break
		}
		s, err := build(bars[trainStart:trainEnd:trainEnd])
		if err != nil {
			return nil, fmt.Errorf("fold %d: %w", k, err)
		}
		res, err := Run(bars[trainStart:testEnd], s, cfg, wf.Train-1)
		if err != nil {
			return nil, fmt.Errorf("fold %d: %w", k, err)
		}
		folds = append(folds, Fold{
			Index:     k,
			TrainFrom: bars[trainStart].Time,
			TestFrom:  bars[trainEnd].Time,
			TestTo:    bars[testEnd-1].Time,
			Result:    res,
		})
		if testEnd == len(bars) {
			break
		}
	}
	if len(folds) == 0 {
		return nil, fmt.Errorf("%w (have %d bars, need >= %d)", ErrInsufficientData, len(bars), wf.Train+2)
	}
	return folds, nil
}
