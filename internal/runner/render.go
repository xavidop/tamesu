package runner

import (
	"fmt"

	"github.com/xavidop/tamesu/internal/config"
)

// modelScope is the run's scope with secrets replaced by their placeholders:
// what the simulator and the judges may see. Secrets are substituted only
// inside the transport (headers, bodies, typed fields), never in a prompt.
func modelScope(sc config.Scope) config.Scope {
	sc.Secrets = func(name string) (string, bool) { return "{{secrets." + name + "}}", true }
	return sc
}

// renderPersona expands the templates of a persona's text fields.
func renderPersona(p *config.Persona, render func(string) (string, error)) (config.Persona, error) {
	if p == nil {
		return config.Persona{}, nil
	}
	out := *p
	var err error
	for _, f := range []*string{&out.Name, &out.Style, &out.Language, &out.Voice} {
		if *f, err = render(*f); err != nil {
			return out, fmt.Errorf("persona: %w", err)
		}
	}
	return out, nil
}

// renderScenario expands the templates of a scenario's text fields.
func renderScenario(s *config.Scenario, render func(string) (string, error)) (config.Scenario, error) {
	if s == nil {
		return config.Scenario{}, nil
	}
	out := *s
	var err error
	if out.Goal, err = render(out.Goal); err != nil {
		return out, fmt.Errorf("scenario.goal: %w", err)
	}
	if out.StopWhen, err = render(out.StopWhen); err != nil {
		return out, fmt.Errorf("scenario.stop_when: %w", err)
	}
	// Script say steps render when they are sent (persona.Loop.Render), so
	// values captured earlier in the conversation are available to them.
	out.Script = append([]config.ScriptStep(nil), s.Script...)
	return out, nil
}
