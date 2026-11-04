// Package profile implements layer 1: the user profile, a fixed-schema input
// vector validated strictly. Nothing outside the schema is accepted.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
)

// Skill is a label from a closed set.
type Skill string

const (
	SkillDerivatives         Skill = "derivatives"
	SkillFundamentalAnalysis Skill = "fundamental_analysis"
	SkillProgramming         Skill = "programming"
	SkillStatistics          Skill = "statistics"
	SkillTechnicalAnalysis   Skill = "technical_analysis"
)

var knownSkills = []Skill{
	SkillDerivatives,
	SkillFundamentalAnalysis,
	SkillProgramming,
	SkillStatistics,
	SkillTechnicalAnalysis,
}

// KnownSkills returns the closed set of accepted skill labels, sorted.
func KnownSkills() []Skill { return slices.Clone(knownSkills) }

// Valid reports whether s belongs to the closed set.
func (s Skill) Valid() bool { return slices.Contains(knownSkills, s) }

const (
	MinRiskTolerance = 1
	MaxRiskTolerance = 10
	MaxHoursPerWeek  = 168

	// drawdownPerRiskStep maps risk tolerance to the maximum acceptable
	// drawdown: tolerance 1 → 5 %, tolerance 10 → 50 %.
	drawdownPerRiskStep = 0.05
)

// Profile is the input vector of the decision pipeline.
//
// Capital is expressed in minor currency units (e.g. cents) to avoid any
// floating-point rounding on money.
type Profile struct {
	Capital       int64   `json:"capital"`
	HoursPerWeek  int     `json:"hours_per_week"`
	Skills        []Skill `json:"skills"`
	RiskTolerance int     `json:"risk_tolerance"`
}

// Validate checks every field against the schema bounds.
func (p Profile) Validate() error {
	var errs []error
	if p.Capital <= 0 {
		errs = append(errs, fmt.Errorf("capital must be > 0 (minor units), got %d", p.Capital))
	}
	if p.HoursPerWeek < 0 || p.HoursPerWeek > MaxHoursPerWeek {
		errs = append(errs, fmt.Errorf("hours_per_week must be in [0,%d], got %d", MaxHoursPerWeek, p.HoursPerWeek))
	}
	if p.RiskTolerance < MinRiskTolerance || p.RiskTolerance > MaxRiskTolerance {
		errs = append(errs, fmt.Errorf("risk_tolerance must be in [%d,%d], got %d", MinRiskTolerance, MaxRiskTolerance, p.RiskTolerance))
	}
	seen := make(map[Skill]bool, len(p.Skills))
	for _, s := range p.Skills {
		if !s.Valid() {
			errs = append(errs, fmt.Errorf("unknown skill %q (allowed: %v)", s, knownSkills))
			continue
		}
		if seen[s] {
			errs = append(errs, fmt.Errorf("duplicate skill %q", s))
		}
		seen[s] = true
	}
	return errors.Join(errs...)
}

// HasSkill reports whether the profile declares skill s.
func (p Profile) HasSkill(s Skill) bool { return slices.Contains(p.Skills, s) }

// MaxDrawdown is the largest peak-to-trough loss (fraction) the user accepts,
// derived linearly from RiskTolerance.
func (p Profile) MaxDrawdown() float64 { return float64(p.RiskTolerance) * drawdownPerRiskStep }

// wire uses pointers so that missing fields are detected instead of silently
// defaulting to zero.
type wire struct {
	Capital       *int64   `json:"capital"`
	HoursPerWeek  *int     `json:"hours_per_week"`
	Skills        *[]Skill `json:"skills"`
	RiskTolerance *int     `json:"risk_tolerance"`
}

// Parse decodes and validates a profile. Unknown fields, missing fields,
// trailing data and out-of-bound values are all rejected.
func Parse(r io.Reader) (Profile, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Profile{}, errors.New("profile: unexpected data after JSON object")
	}
	var missing []string
	if w.Capital == nil {
		missing = append(missing, "capital")
	}
	if w.HoursPerWeek == nil {
		missing = append(missing, "hours_per_week")
	}
	if w.Skills == nil || *w.Skills == nil {
		missing = append(missing, "skills")
	}
	if w.RiskTolerance == nil {
		missing = append(missing, "risk_tolerance")
	}
	if len(missing) > 0 {
		return Profile{}, fmt.Errorf("profile: missing required fields %v", missing)
	}
	p := Profile{
		Capital:       *w.Capital,
		HoursPerWeek:  *w.HoursPerWeek,
		Skills:        *w.Skills,
		RiskTolerance: *w.RiskTolerance,
	}
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	return p, nil
}

// Load reads and parses a profile file.
func Load(path string) (Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Profile{}, err
	}
	defer f.Close()
	return Parse(f)
}
