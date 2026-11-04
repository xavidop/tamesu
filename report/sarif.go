package report

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SARIFFile is the file the sarif reporter writes into the run directory.
const SARIFFile = "tamesu.sarif"

// SARIF writes the run's security findings as SARIF 2.1.0, the format
// GitHub code scanning, Azure DevOps and most security dashboards ingest:
// every red-team attack that got through, every attack that could not run,
// and every failed check of a red-team pack on any test. A run without
// findings writes an empty result set, which closes the open alerts.
type SARIF struct{ dir string }

// NewSARIF returns the SARIF reporter for the run directory.
func NewSARIF(dir string) *SARIF { return &SARIF{dir: dir} }

func (*SARIF) RunStarted(RunInfo)                    {}
func (*SARIF) TestStarted(*TestOutcome)              {}
func (*SARIF) RunFinished(*TestOutcome, *RunOutcome) {}
func (*SARIF) TestFinished(*TestOutcome)             {}

// SARIF 2.1.0, as far as the reporter writes it.

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	ShortDescription sarifText        `json:"shortDescription"`
	FullDescription  sarifText        `json:"fullDescription,omitempty"`
	HelpURI          string           `json:"helpUri,omitempty"`
	Properties       map[string]any   `json:"properties,omitempty"`
	DefaultConfig    *sarifRuleConfig `json:"defaultConfiguration,omitempty"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
	Properties          map[string]any    `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// Finding is one security finding of a run, before it is rendered.
type Finding struct {
	// Rule is the rule id: tamesu/playbook/<name> for a red-team attack,
	// tamesu/pack/<name> for a failed pack check.
	Rule string
	// Playbook, OWASP and Pack say where the finding comes from.
	Playbook, OWASP, Pack string
	// Attack is the attack's name (playbook findings).
	Attack string
	// Level is error (got through / failed) or warning (could not run).
	Level string
	// Message says what happened, with the judge's reasons.
	Message string
	Test    *TestOutcome
}

// BuildFindings lists the run's security findings: red-team attacks that
// got through or could not run, and failed checks of red-team packs.
func BuildFindings(s *Summary) []Finding {
	var out []Finding
	for _, t := range s.Tests {
		if t.Skipped() {
			continue
		}
		playbook, owasp := "", ""
		for _, tag := range t.Tags {
			if v, ok := strings.CutPrefix(tag, "playbook:"); ok {
				playbook = v
			}
			if v, ok := strings.CutPrefix(tag, "owasp:"); ok {
				owasp = v
			}
		}
		if playbook != "" {
			attack := strings.TrimPrefix(t.Name, "red team: "+playbook+"/")
			switch {
			case t.Passed || t.Quarantined():
			case t.Errored():
				out = append(out, Finding{Rule: "tamesu/playbook/" + playbook, Playbook: playbook, OWASP: owasp, Attack: attack, Level: "warning", Test: t,
					Message: fmt.Sprintf("The %s attack %q could not run: %s.", playbook, attack, firstError(t))})
			default:
				out = append(out, Finding{Rule: "tamesu/playbook/" + playbook, Playbook: playbook, OWASP: owasp, Attack: attack, Level: "error", Test: t,
					Message: fmt.Sprintf("The %s attack %q got through: %s", playbook, attack, reasons(t))})
			}
			continue
		}
		if t.Passed || t.Quarantined() || t.Errored() {
			continue
		}
		// A failed check of a red-team pack on an ordinary test.
		seen := map[string]bool{}
		for _, r := range t.Runs {
			for _, a := range r.FailedAssertions() {
				pack := packOf(a.Name)
				if pack == "" || seen[pack] {
					continue
				}
				seen[pack] = true
				out = append(out, Finding{Rule: "tamesu/pack/" + pack, Pack: pack, Level: "error", Test: t,
					Message: fmt.Sprintf("Test %q failed the %s check %s: %s", t.Name, pack, a.Name, strings.TrimSpace(a.Reason))})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Test.Name < out[j].Test.Name
	})
	return out
}

// securityPacks are the built-in packs whose failures are security findings.
var securityPacks = map[string]bool{
	"prompt_injection": true, "pii_leakage": true, "system_prompt_leakage": true, "stays_in_scope": true,
	"discloses_ai": true, "harmful_content": true, "excessive_agency": true, "policy_consistency": true,
}

// packOf is the red-team pack an assertion name belongs to
// (<pack>/<check> or defaults/<pack>/<check>), or "".
func packOf(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return ""
	}
	pack := parts[0]
	if pack == "defaults" && len(parts) >= 3 {
		pack = parts[1]
	}
	if securityPacks[pack] {
		return pack
	}
	return ""
}

func firstError(t *TestOutcome) string {
	for _, r := range t.Runs {
		if r.Error != "" {
			return r.ErrorKind + ": " + r.Error
		}
	}
	return "infrastructure error"
}

// reasons joins the failing assertions of a test's failed runs.
func reasons(t *TestOutcome) string {
	var parts []string
	seen := map[string]bool{}
	for _, r := range t.Runs {
		if r.Passed {
			continue
		}
		for _, a := range r.FailedAssertions() {
			line := a.Name
			if reason := strings.TrimSpace(a.Reason); reason != "" {
				line += ": " + reason
			}
			if !seen[line] {
				seen[line] = true
				parts = append(parts, line)
			}
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d of %d runs passed.", passedRuns(t), len(t.Runs))
	}
	return strings.Join(parts, " ")
}

func passedRuns(t *TestOutcome) int {
	n := 0
	for _, r := range t.Runs {
		if r.Passed {
			n++
		}
	}
	return n
}

// Build renders the summary as SARIF.
func (*SARIF) Build(s *Summary) ([]byte, error) {
	findings := BuildFindings(s)
	driver := sarifDriver{Name: "tamesu", Version: s.Version, InformationURI: "https://xavidop.github.io/tamesu/docs/red-team/"}
	ruleIndex := map[string]int{}
	results := make([]sarifResult, 0, len(findings))
	for _, f := range findings {
		idx, ok := ruleIndex[f.Rule]
		if !ok {
			idx = len(driver.Rules)
			ruleIndex[f.Rule] = idx
			driver.Rules = append(driver.Rules, ruleFor(f))
		}
		res := sarifResult{RuleID: f.Rule, RuleIndex: idx, Level: f.Level, Message: sarifText{Text: f.Message},
			PartialFingerprints: map[string]string{"tamesu/v1": f.Test.Suite + "|" + f.Test.Name},
			Properties:          map[string]any{"suite": f.Test.Suite, "test": f.Test.Name, "target": f.Test.Target}}
		if f.OWASP != "" {
			res.Properties["owasp"] = f.OWASP
		}
		if f.Attack != "" {
			res.Properties["attack"] = f.Attack
		}
		if f.Test.File != "" {
			loc := sarifLocation{PhysicalLocation: sarifPhysical{ArtifactLocation: sarifArtifact{URI: relativeURI(f.Test.File)}}}
			if f.Test.Line > 0 {
				loc.PhysicalLocation.Region = &sarifRegion{StartLine: f.Test.Line}
			}
			res.Locations = []sarifLocation{loc}
		}
		results = append(results, res)
	}
	if driver.Rules == nil {
		driver.Rules = []sarifRule{}
	}
	log := sarifLog{Schema: "https://json.schemastore.org/sarif-2.1.0.json", Version: "2.1.0", Runs: []sarifRun{{Tool: sarifTool{Driver: driver}, Results: results}}}
	b, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ruleFor describes a finding's rule: the playbook with its OWASP category,
// or the pack.
func ruleFor(f Finding) sarifRule {
	tags := []string{"security", "tamesu"}
	switch {
	case f.Playbook != "":
		title := f.Playbook
		if t, ok := OWASPTitles[f.OWASP]; ok {
			title = fmt.Sprintf("%s (%s %s)", f.Playbook, f.OWASP, t)
		}
		if f.OWASP != "" {
			tags = append(tags, "owasp:"+f.OWASP)
		}
		return sarifRule{ID: f.Rule, Name: "Playbook" + camel(f.Playbook), ShortDescription: sarifText{Text: "Red-team playbook " + title},
			FullDescription: sarifText{Text: fmt.Sprintf("An attack of the %s playbook got through the agent, or could not be run. Each result names the attack and quotes the judge's reasons; the test's transcript is in the run directory.", f.Playbook)},
			HelpURI:         "https://xavidop.github.io/tamesu/docs/red-team/#playbooks", Properties: map[string]any{"tags": tags, "precision": "medium", "problem.severity": "error"},
			DefaultConfig: &sarifRuleConfig{Level: "error"}}
	default:
		return sarifRule{ID: f.Rule, Name: "Pack" + camel(f.Pack), ShortDescription: sarifText{Text: "Red-team pack " + f.Pack},
			FullDescription: sarifText{Text: fmt.Sprintf("A test failed a check of the %s pack. The result names the check and quotes the judge's reason.", f.Pack)},
			HelpURI:         "https://xavidop.github.io/tamesu/docs/red-team/#packs", Properties: map[string]any{"tags": tags, "precision": "medium", "problem.severity": "error"},
			DefaultConfig: &sarifRuleConfig{Level: "error"}}
	}
}

func camel(s string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// relativeURI makes a suite path relative to the working directory, as code
// scanning expects a path inside the repository.
func relativeURI(path string) string {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(strings.TrimPrefix(path, "./"))
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

// Finish writes tamesu.sarif.
func (r *SARIF) Finish(_ context.Context, s *Summary) error {
	b, err := r.Build(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.dir, SARIFFile), b, 0o644)
}
