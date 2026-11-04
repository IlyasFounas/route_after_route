package backtest

import "math"

// Sharpe returns the annualised Sharpe ratio (risk-free rate 0) along with
// the per-period mean and sample standard deviation. A zero-variance series
// has a Sharpe of 0.
func Sharpe(returns []float64, periodsPerYear float64) (sharpe, mean, std float64) {
	n := float64(len(returns))
	if n < 2 {
		return 0, 0, 0
	}
	for _, r := range returns {
		mean += r
	}
	mean /= n
	ss := 0.0
	for _, r := range returns {
		ss += (r - mean) * (r - mean)
	}
	std = math.Sqrt(ss / (n - 1))
	if std == 0 {
		return 0, mean, 0
	}
	return mean / std * math.Sqrt(periodsPerYear), mean, std
}

// MaxDrawdown returns the largest peak-to-trough loss of the compounded
// equity curve, as a positive fraction.
func MaxDrawdown(returns []float64) float64 {
	equity, peak, dd := 1.0, 1.0, 0.0
	for _, r := range returns {
		equity *= 1 + r
		peak = math.Max(peak, equity)
		dd = math.Max(dd, 1-equity/peak)
	}
	return dd
}

// TotalReturn compounds the returns.
func TotalReturn(returns []float64) float64 {
	e := 1.0
	for _, r := range returns {
		e *= 1 + r
	}
	return e - 1
}
