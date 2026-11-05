package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/secrets"
)

// SIPSetupRequest is what `tamesu sip setup` hands the sip module.
type SIPSetupRequest struct {
	Project    config.Project
	ConfigPath string
	// LiveKitURL and the API keys, resolved.
	LiveKitURL, APIKey, APISecret string
	// Render expands {{secrets.X}} and {{vars.x}} in the trunk's auth fields.
	Render func(string) (string, error)
	// Outbound and Inbound select what to set up; both when neither is set.
	Outbound, Inbound bool
}

// SIPSetupResult is what exists after setup.
type SIPSetupResult struct {
	OutboundTrunkID, InboundTrunkID, DispatchRuleID string
	Created, Reused                                 []string
	// YAML is the tamesu.yaml snippet to paste.
	YAML string
}

func (a *app) newSIPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sip",
		Short: "Phone target helpers: set up the LiveKit SIP trunks and dispatch rule",
		Long: "The sip target calls your agent through a LiveKit SIP service and a trunk you\n" +
			"bring. `tamesu sip setup` creates (or reuses) the outbound trunk, the inbound\n" +
			"trunk and the dispatch rule from sip.trunk in tamesu.yaml and prints the ids.",
	}
	cmd.AddCommand(a.newSIPSetupCmd())
	return cmd
}

func (a *app) newSIPSetupCmd() *cobra.Command {
	var outbound, inbound bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Create or reuse the LiveKit SIP trunks and the inbound dispatch rule (idempotent)",
		Long: "Setup reads sip.livekit_url and sip.trunk from tamesu.yaml, resolves\n" +
			"LIVEKIT_API_KEY, LIVEKIT_API_SECRET and the trunk's auth secrets through mamori,\n" +
			"then creates what does not exist yet, by name: the outbound trunk\n" +
			"(tamesu-outbound), the inbound trunk (tamesu-inbound) and the dispatch rule that\n" +
			"routes inbound calls into sip.inbound_room. Running it again changes nothing. It\n" +
			"prints the ids as the sip: block to put in tamesu.yaml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.deps.SIPSetup == nil {
				return configError(errors.New("sip setup is not available in this build"))
			}
			p, perrs, err := a.loadProject(cmd, true)
			if err != nil {
				return err
			}
			if perrs.HasErrors() {
				return configError(perrs)
			}
			if p.SIP.LiveKitURL == "" {
				return configError(errors.New("sip.livekit_url is not set in " + a.configPath))
			}
			if p.SIP.Trunk == nil {
				return configError(errors.New("sip.trunk is not set in " + a.configPath + " (address, numbers, auth_username, auth_password)"))
			}
			flagVars, err := a.refVars(nil)
			if err != nil {
				return err
			}
			// The keys and whatever the trunk's auth fields reference.
			want := map[string]bool{"LIVEKIT_API_KEY": true, "LIVEKIT_API_SECRET": true}
			for _, f := range []string{p.SIP.Trunk.AuthUsername, p.SIP.Trunk.AuthPassword} {
				for _, name := range config.SecretRefs(f) {
					want[name] = true
				}
			}
			refs := map[string]string{}
			for name := range want {
				if ref, ok := p.Secrets[name]; ok {
					refs[name] = ref
				}
			}
			store, err := secrets.Resolve(cmd.Context(), refs, secrets.Options{RefVars: flagVars, EnvFile: a.envFile, Providers: a.deps.Providers})
			if err != nil {
				return err
			}
			for _, name := range store.Names() {
				if v, ok := store.Lookup(name); ok {
					a.redactor.Add(name, v)
				}
			}
			lookup := func(name string) (string, bool) {
				if v, ok := store.Lookup(name); ok {
					return v, true
				}
				return a.env()(name)
			}
			key, okK := lookup("LIVEKIT_API_KEY")
			secret, okS := lookup("LIVEKIT_API_SECRET")
			if !okK || !okS || key == "" || secret == "" {
				return configError(errors.New("LIVEKIT_API_KEY and LIVEKIT_API_SECRET must be declared under secrets: (or set in the environment)"))
			}
			scope := config.Scope{Vars: flagVars, Env: a.env(), Secrets: lookup}
			res, err := a.deps.SIPSetup(cmd.Context(), SIPSetupRequest{
				Project: p, ConfigPath: a.configPath, LiveKitURL: p.SIP.LiveKitURL, APIKey: key, APISecret: secret,
				Render:   func(s string) (string, error) { return config.Render(s, scope) },
				Outbound: outbound, Inbound: inbound,
			})
			if err != nil {
				return infraError(err)
			}
			out := cmd.OutOrStdout()
			st := a.style(out)
			for _, c := range res.Created {
				_, _ = fmt.Fprintf(out, "%s %s\n", st.Pass("created"), c)
			}
			for _, r := range res.Reused {
				_, _ = fmt.Fprintf(out, "%s  %s\n", st.Dim("reused"), r)
			}
			_, _ = fmt.Fprintf(out, "\n# put this in %s:\n%s", a.configPath, strings.TrimRight(res.YAML, "\n")+"\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&outbound, "outbound", false, "set up the outbound trunk only")
	cmd.Flags().BoolVar(&inbound, "inbound", false, "set up the inbound trunk and dispatch rule only")
	return cmd
}
