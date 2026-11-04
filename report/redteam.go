package report

import (
	"fmt"
	"sort"
	"strings"
)

// PlaybookResult is how one red-team playbook fared: its attacks (tests
// tagged playbook:<name>), how many held, and the ones that got through.
type PlaybookResult struct {
	Name    string   `json:"name"`
	OWASP   string   `json:"owasp,omitempty"`
	Attacks int      `json:"attacks"`
	Held    int      `json:"held"`
	Broken  []string `json:"broken,omitempty"`
	Errored []string `json:"errored,omitempty"`
}

// OWASPTitles names the OWASP Top 10 for LLM Applications categories the
// playbooks map to.
var OWASPTitles = map[string]string{
	"LLM01": "Prompt Injection", "LLM02": "Sensitive Information Disclosure", "LLM03": "Supply Chain",
	"LLM04": "Data and Model Poisoning", "LLM05": "Improper Output Handling", "LLM06": "Excessive Agency",
	"LLM07": "System Prompt Leakage", "LLM08": "Vector and Embedding Weaknesses", "LLM09": "Misinformation",
	"LLM10": "Unbounded Consumption",
}

// BuildRedTeam groups a run's red-team tests by playbook (the playbook:<name>
// tag the red_team block sets) and reports which attacks got through. Nil
// when the run had no playbook tests.
func BuildRedTeam(s *Summary) []PlaybookResult {
	byName := map[string]*PlaybookResult{}
	for _, t := range s.Tests {
		name, owasp := "", ""
		for _, tag := range t.Tags {
			if v, ok := strings.CutPrefix(tag, "playbook:"); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(tag, "owasp:"); ok {
				owasp = v
			}
		}
		if name == "" {
			continue
		}
		r, ok := byName[name]
		if !ok {
			r = &PlaybookResult{Name: name, OWASP: owasp}
			byName[name] = r
		}
		r.Attacks++
		attack := strings.TrimPrefix(t.Name, "red team: "+name+"/")
		switch {
		case t.Skipped():
			r.Attacks--
		case t.Errored():
			r.Errored = append(r.Errored, attack)
		case t.Passed || t.Quarantined():
			r.Held++
		default:
			r.Broken = append(r.Broken, attack)
		}
	}
	if len(byName) == 0 {
		return nil
	}
	out := make([]PlaybookResult, 0, len(byName))
	for _, r := range byName {
		sort.Strings(r.Broken)
		sort.Strings(r.Errored)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OWASP != out[j].OWASP {
			return out[i].OWASP < out[j].OWASP
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RedTeamMarkdown renders the playbook table for a report; "" without
// playbooks.
func RedTeamMarkdown(results []PlaybookResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Red team\n\n| Playbook | OWASP | Attacks | Held | Got through |\n|---|---|---|---|---|\n")
	for _, r := range results {
		through := strings.Join(r.Broken, ", ")
		if len(r.Errored) > 0 {
			through = strings.TrimPrefix(through+", errors: "+strings.Join(r.Errored, ", "), ", ")
		}
		if through == "" {
			through = "none"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %s |\n", r.Name, owaspCell(r.OWASP), r.Attacks, r.Held, mdEscape(through))
	}
	b.WriteString("\n")
	return b.String()
}

// RedTeamConsole renders the playbook lines for a terminal; "" without
// playbooks.
func RedTeamConsole(results []PlaybookResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nred team\n")
	for _, r := range results {
		status := "held"
		if len(r.Broken) > 0 {
			status = "got through: " + strings.Join(r.Broken, ", ")
		}
		if len(r.Errored) > 0 {
			status += " · errors: " + strings.Join(r.Errored, ", ")
		}
		fmt.Fprintf(&b, "  %-28s %-7s %d/%d attacks held · %s\n", r.Name, r.OWASP, r.Held, r.Attacks, status)
	}
	return b.String()
}

// RedTeamLine is the one-line verdict for the console summary: "" without
// playbooks.
func RedTeamLine(results []PlaybookResult) string {
	if len(results) == 0 {
		return ""
	}
	attacks, held := 0, 0
	var through []string
	for _, r := range results {
		attacks += r.Attacks
		held += r.Held
		for _, a := range r.Broken {
			through = append(through, r.Name+"/"+a)
		}
	}
	line := fmt.Sprintf("red team: %d of %d attacks held", held, attacks)
	if len(through) > 0 {
		line += " · got through: " + strings.Join(through, ", ")
	}
	return line
}

func owaspCell(id string) string {
	if id == "" {
		return ""
	}
	if title, ok := OWASPTitles[id]; ok {
		return id + " " + title
	}
	return id
}
