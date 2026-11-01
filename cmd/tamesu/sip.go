package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/sip"
)

func init() {
	targetFactories[config.TargetSIP] = newSIPTarget
}

// newSIPTarget maps a suite's sip target and the project's sip: block onto
// the sip module's config.
func newSIPTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	p := spec.Project.SIP
	cfg := sip.Config{
		LiveKitURL: p.LiveKitURL, APIKey: "{{secrets.LIVEKIT_API_KEY}}", APISecret: "{{secrets.LIVEKIT_API_SECRET}}",
		Direction: t.Direction, To: t.To, From: t.From, TrunkID: t.TrunkID,
		RingTimeout: t.RingTimeout.Std(), MaxCallDuration: t.MaxCallDuration.Std(),
		InboundNumber: p.InboundNumber, InboundRoom: p.InboundRoom, AllowedNumbers: p.AllowedNumbers,
	}
	if cfg.TrunkID == "" {
		cfg.TrunkID = p.OutboundTrunkID
	}
	if t.Trigger != nil {
		cfg.Trigger = &sip.Trigger{Command: t.Trigger.Command}
		if h := t.Trigger.HTTP; h != nil {
			cfg.Trigger.HTTP = &sip.HTTPTrigger{URL: h.URL, Method: h.Method, Body: h.Body, Headers: h.Headers}
		}
	}
	return sip.New(spec.Name, cfg), nil
}

// sipSetup is `tamesu sip setup`: create or reuse the trunks and dispatch rule.
func sipSetup(ctx context.Context, req cli.SIPSetupRequest) (cli.SIPSetupResult, error) {
	tr := req.Project.SIP.Trunk
	if tr == nil {
		return cli.SIPSetupResult{}, fmt.Errorf("sip.trunk is not configured in %s (address, numbers, auth_username, auth_password)", req.ConfigPath)
	}
	user, err := req.Render(tr.AuthUsername)
	if err != nil {
		return cli.SIPSetupResult{}, fmt.Errorf("sip.trunk.auth_username: %w", err)
	}
	pass, err := req.Render(tr.AuthPassword)
	if err != nil {
		return cli.SIPSetupResult{}, fmt.Errorf("sip.trunk.auth_password: %w", err)
	}
	api := sip.NewSetupAPI(req.LiveKitURL, req.APIKey, req.APISecret)
	res, err := sip.Setup(ctx, api, sip.SetupConfig{
		Trunk: sip.TrunkConfig{
			Address: tr.Address, Numbers: tr.Numbers, InboundNumbers: tr.InboundNumbers,
			AuthUsername: user, AuthPassword: pass, Transport: tr.Transport, AllowedAddresses: tr.AllowedAddresses,
		},
		InboundRoom: req.Project.SIP.InboundRoom,
		Outbound:    req.Outbound, Inbound: req.Inbound,
	})
	if err != nil {
		return cli.SIPSetupResult{}, err
	}
	return cli.SIPSetupResult{OutboundTrunkID: res.OutboundTrunkID, InboundTrunkID: res.InboundTrunkID, DispatchRuleID: res.DispatchRuleID, Created: res.Created, Reused: res.Reused, YAML: res.YAML()}, nil
}

// checkSIP is the `tamesu doctor` check for the phone target: the project's
// sip block is complete for the targets the suites declare.
func checkSIP(_ context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	var outbound, inbound int
	for _, s := range env.Suites {
		for _, t := range s.Targets {
			if t.Type != config.TargetSIP {
				continue
			}
			if t.Direction == sip.Inbound {
				inbound++
			} else {
				outbound++
			}
		}
	}
	if outbound == 0 && inbound == 0 {
		return []cli.DoctorResult{{Name: "sip", Status: cli.StatusSkip, Detail: "no sip target configured"}}
	}
	p := env.Project.SIP
	var problems, details []string
	if p.LiveKitURL == "" {
		problems = append(problems, "sip.livekit_url is not set")
	} else {
		details = append(details, "livekit_url="+p.LiveKitURL)
	}
	if outbound > 0 {
		if p.OutboundTrunkID == "" {
			problems = append(problems, fmt.Sprintf("%d outbound target(s) but no sip.outbound_trunk_id (run tamesu sip setup)", outbound))
		} else {
			details = append(details, "outbound_trunk_id="+p.OutboundTrunkID)
		}
		if len(p.AllowedNumbers) == 0 {
			details = append(details, "allowed_numbers unset: only numbers written in the suites can be dialed")
		} else {
			details = append(details, fmt.Sprintf("allowed_numbers=%s", strings.Join(p.AllowedNumbers, ",")))
		}
	}
	if inbound > 0 {
		if p.InboundNumber == "" {
			problems = append(problems, fmt.Sprintf("%d inbound target(s) but no sip.inbound_number", inbound))
		} else {
			details = append(details, "inbound_number="+p.InboundNumber)
		}
	}
	for _, name := range []string{"LIVEKIT_API_KEY", "LIVEKIT_API_SECRET"} {
		if !hasSecret(env.Secrets, name) {
			problems = append(problems, name+" is not declared under secrets:")
		}
	}
	if len(problems) > 0 {
		return []cli.DoctorResult{{Name: "sip", Status: cli.StatusFail, Detail: strings.Join(append(problems, details...), ", ")}}
	}
	return []cli.DoctorResult{{Name: "sip", Status: cli.StatusOK, Detail: strings.Join(details, ", ")}}
}
