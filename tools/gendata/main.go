// Command gendata regenerates the deterministic synthetic markets in
// testdata/trend and testdata/range (fixed seed). Run: go run ./tools/gendata
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/market"
	"github.com/IlyasFounas/route_after_route/internal/profile"
)

const nBars = 1000

var start = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	root := flag.String("out", "testdata", "output root directory")
	flag.Parse()
	rng := rand.New(rand.NewPCG(42, 2020))

	// TREND: regime-switching drift (trend-following friendly).
	trend := walk(rng, nBars, 100, func(i int, p float64) float64 {
		drift := 0.0025
		if (i/150)%2 == 1 {
			drift = -0.0015
		}
		return p * math.Exp(drift+0.015*rng.NormFloat64())
	})
	// RANGE: Ornstein-Uhlenbeck around 100 (mean-reversion friendly).
	rangeS := walk(rng, nBars, 100, func(_ int, p float64) float64 {
		return math.Max(1, p+0.15*(100-p)+1.6*rng.NormFloat64())
	})
	// DEAD: declining, delisted after 600 bars (survivorship-bias guard).
	const deadBars = 600
	dead := walk(rng, deadBars, 50, func(_ int, p float64) float64 {
		return p * math.Exp(-0.0015+0.03*rng.NormFloat64())
	})

	delisted := start.AddDate(0, 0, deadBars)
	trendDir, rangeDir := filepath.Join(*root, "trend"), filepath.Join(*root, "range")
	write(rng, filepath.Join(trendDir, "TREND.csv"), trend)
	write(rng, filepath.Join(trendDir, "DEAD.csv"), dead)
	write(rng, filepath.Join(rangeDir, "RANGE.csv"), rangeS)

	writeDescriptor(filepath.Join(trendDir, "market.json"), market.Descriptor{
		ID:          "demo-trend",
		Name:        "Synthetic trending market (deterministic, seed 42)",
		Kind:        market.KindCrypto,
		Granularity: market.Day,
		Fees:        market.Fees{TakerBps: 10, MakerBps: 8},
		MinCapital:  10000,
		Access:      market.Access{ShortAllowed: true},
		Symbols: []market.Symbol{
			{Name: "TREND", Listed: start},
			{Name: "DEAD", Listed: start, Delisted: &delisted},
		},
		DataSource: "tools/gendata",
	})
	writeDescriptor(filepath.Join(rangeDir, "market.json"), market.Descriptor{
		ID:          "demo-range",
		Name:        "Synthetic range-bound market (deterministic, seed 42)",
		Kind:        market.KindCrypto,
		Granularity: market.Day,
		Fees:        market.Fees{TakerBps: 10, MakerBps: 8},
		MinCapital:  50000,
		Access:      market.Access{ShortAllowed: true, RequiredSkills: []profile.Skill{profile.SkillStatistics}, MinHoursPerWeek: 2},
		Symbols:     []market.Symbol{{Name: "RANGE", Listed: start}},
		DataSource:  "tools/gendata",
	})
}

func writeDescriptor(path string, d market.Descriptor) {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
}

func walk(_ *rand.Rand, n int, p0 float64, step func(int, float64) float64) []float64 {
	out := make([]float64, n)
	p := p0
	for i := range out {
		out[i] = p
		p = step(i, p)
	}
	return out
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

func write(rng *rand.Rand, path string, closes []float64) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"time", "open", "high", "low", "close", "volume"})
	prev := closes[0]
	for i, c := range closes {
		o := round4(prev * (1 + 0.003*rng.NormFloat64()))
		c = round4(c)
		h := round4(math.Max(o, c) * (1 + 0.006*math.Abs(rng.NormFloat64())))
		l := round4(math.Min(o, c) * (1 - 0.006*math.Abs(rng.NormFloat64())))
		v := math.Round(1e6 * math.Exp(0.3*rng.NormFloat64()))
		ff := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
		w.Write([]string{start.AddDate(0, 0, i).Format(time.DateOnly), ff(o), ff(h), ff(l), ff(c), ff(v)})
		prev = c
	}
	w.Flush()
	if err := w.Error(); err != nil {
		log.Fatal(err)
	}
}
