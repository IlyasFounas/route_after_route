package profile

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const valid = `{"capital":100000,"hours_per_week":5,"skills":["statistics"],"risk_tolerance":4}`

func TestParseValid(t *testing.T) {
	p, err := Parse(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if p.Capital != 100000 || p.RiskTolerance != 4 || !p.HasSkill(SkillStatistics) {
		t.Fatalf("unexpected profile %+v", p)
	}
	if got := p.MaxDrawdown(); got != 0.2 {
		t.Fatalf("MaxDrawdown = %v, want 0.2", got)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"capital":1,"hours_per_week":1,"skills":[],"risk_tolerance":1,"extra":1}`,
		"missing field":   `{"capital":1,"hours_per_week":1,"skills":[]}`,
		"null skills":     `{"capital":1,"hours_per_week":1,"skills":null,"risk_tolerance":1}`,
		"zero capital":    `{"capital":0,"hours_per_week":1,"skills":[],"risk_tolerance":1}`,
		"risk too high":   `{"capital":1,"hours_per_week":1,"skills":[],"risk_tolerance":11}`,
		"risk too low":    `{"capital":1,"hours_per_week":1,"skills":[],"risk_tolerance":0}`,
		"hours too high":  `{"capital":1,"hours_per_week":169,"skills":[],"risk_tolerance":1}`,
		"unknown skill":   `{"capital":1,"hours_per_week":1,"skills":["astrology"],"risk_tolerance":1}`,
		"duplicate skill": `{"capital":1,"hours_per_week":1,"skills":["statistics","statistics"],"risk_tolerance":1}`,
		"float capital":   `{"capital":1.5,"hours_per_week":1,"skills":[],"risk_tolerance":1}`,
		"trailing data":   valid + `{}`,
		"not an object":   `[]`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(in)); err == nil {
				t.Fatalf("expected error for %s", in)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	f.Add(valid)
	f.Add(`{"capital":-1}`)
	f.Add(`{"capital":1,"hours_per_week":0,"skills":[],"risk_tolerance":10}`)
	f.Fuzz(func(t *testing.T, in string) {
		p, err := Parse(strings.NewReader(in))
		if err != nil {
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("Parse accepted an invalid profile: %v", err)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		q, err := Parse(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("round-trip failed: %v (%s)", err, raw)
		}
		if !reflect.DeepEqual(p, q) {
			t.Fatalf("round-trip mismatch: %+v vs %+v", p, q)
		}
	})
}
