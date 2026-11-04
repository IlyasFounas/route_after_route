package market

import (
	"testing"
	"time"

	"github.com/IlyasFounas/route_after_route/internal/profile"
)

func day(i int) time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i) }

func TestValidateBars(t *testing.T) {
	ok := []Bar{
		{Time: day(0), Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 1},
		{Time: day(1), Open: 10.5, High: 12, Low: 10, Close: 11, Volume: 0},
	}
	if err := ValidateBars(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]Bar{
		"not increasing": {ok[1], ok[0]},
		"zero price":     {{Time: day(0), Open: 0, High: 1, Low: 0, Close: 1}},
		"high below":     {{Time: day(0), Open: 10, High: 9, Low: 8, Close: 10}},
		"neg volume":     {{Time: day(0), Open: 10, High: 10, Low: 10, Close: 10, Volume: -1}},
	}
	for name, b := range bad {
		if err := ValidateBars(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCompatible(t *testing.T) {
	d := Descriptor{MinCapital: 1000, Access: Access{MinHoursPerWeek: 2, RequiredSkills: []profile.Skill{profile.SkillDerivatives}}}
	p := profile.Profile{Capital: 500, HoursPerWeek: 1, RiskTolerance: 3}
	if got := Compatible(d, p); len(got) != 3 {
		t.Fatalf("want 3 reasons, got %v", got)
	}
	p = profile.Profile{Capital: 1000, HoursPerWeek: 2, Skills: []profile.Skill{profile.SkillDerivatives}, RiskTolerance: 3}
	if got := Compatible(d, p); len(got) != 0 {
		t.Fatalf("want compatible, got %v", got)
	}
}

func TestSymbolLifetime(t *testing.T) {
	del := day(10)
	s := Symbol{Name: "X", Listed: day(5), Delisted: &del}
	if s.ActiveAt(day(4)) || !s.ActiveAt(day(5)) || s.ActiveAt(day(10)) {
		t.Fatal("ActiveAt wrong")
	}
	if !s.OverlapsPeriod(day(0), day(6)) || s.OverlapsPeriod(day(10), day(20)) || s.OverlapsPeriod(day(0), day(5)) {
		t.Fatal("OverlapsPeriod wrong")
	}
}

func TestPeriodsPerYear(t *testing.T) {
	cases := []struct {
		k    Kind
		g    Granularity
		want float64
	}{
		{KindCrypto, Day, 365},
		{KindCrypto, Hour, 365 * 24},
		{KindEquity, Day, 252},
		{KindEquity, Hour, 252 * 6.5},
		{KindFX, Week, 52},
	}
	for _, c := range cases {
		if got := (Descriptor{Kind: c.k, Granularity: c.g}).PeriodsPerYear(); got != c.want {
			t.Errorf("%s/%s: got %v want %v", c.k, c.g, got, c.want)
		}
	}
}
