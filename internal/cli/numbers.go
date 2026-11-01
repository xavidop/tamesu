package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// checkAllowedNumbers refuses a run that would dial a number outside
// sip.allowed_numbers: every selected outbound sip test's `to` is rendered
// with the vars and environment (secrets are not resolved yet; a number that
// needs one is checked again by the target before dialing) and matched. A
// template with no allowed_numbers at all is refused by the validator.
func checkAllowedNumbers(p config.Project, suites []*config.Suite, filter *regexp.Regexp, tags []string, targetName string, flagVars map[string]string, env config.EnvLookup) error {
	for _, s := range suites {
		for _, t := range s.SelectTests(filter, tags, targetName) {
			tgt, ok := s.Targets[t.Target]
			if !ok || tgt.Type != config.TargetSIP || tgt.Direction == "inbound" || tgt.To == "" {
				continue
			}
			vars := map[string]string{}
			for k, v := range s.Vars {
				vars[k] = v
			}
			for k, v := range flagVars {
				vars[k] = v
			}
			if len(config.SecretRefs(tgt.To)) > 0 {
				continue // rendered and checked by the target once the secrets are resolved
			}
			to, err := config.Render(tgt.To, config.Scope{Vars: vars, Env: env})
			if err != nil {
				return fmt.Errorf("%s: test %q: to: %w", s.Path, t.Name, err)
			}
			if (target.Templated(tgt.To) || len(p.SIP.AllowedNumbers) > 0) && !target.NumberAllowed(to, p.SIP.AllowedNumbers) {
				return fmt.Errorf("%s: test %q would dial %s, which is not in sip.allowed_numbers (%s); refusing", s.Path, t.Name, to, strings.Join(p.SIP.AllowedNumbers, ", "))
			}
		}
	}
	return nil
}
