package sip

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Names of the objects `tamesu sip setup` owns; it reuses what exists.
const (
	OutboundTrunkName = "tamesu-outbound"
	InboundTrunkName  = "tamesu-inbound"
	DispatchRuleName  = "tamesu-inbound"
)

// TrunkConfig is the trunk you bring (sip.trunk in tamesu.yaml).
type TrunkConfig struct {
	// Address is the provider's SIP host (sip.twilio.com, my-pbx.example.com:5060).
	Address string
	// Numbers are the trunk's numbers (E.164); the outbound caller IDs and
	// the inbound numbers.
	Numbers []string
	// AuthUsername and AuthPassword authenticate outbound calls (rendered
	// secrets).
	AuthUsername, AuthPassword string
	// Transport is auto, udp, tcp or tls.
	Transport string
	// AllowedAddresses restricts the inbound trunk to the provider's IPs or
	// hostnames (CIDR or names); empty accepts calls from anywhere for the
	// numbers.
	AllowedAddresses []string
	// InboundNumbers overrides Numbers for the inbound trunk.
	InboundNumbers []string
}

// SetupConfig is what Setup needs.
type SetupConfig struct {
	Trunk TrunkConfig
	// InboundRoom is where the dispatch rule routes calls (default tamesu-sip-inbound).
	InboundRoom string
	// Outbound and Inbound select what to set up (both when neither is set).
	Outbound, Inbound bool
}

// SetupResult is what exists after Setup.
type SetupResult struct {
	OutboundTrunkID string
	InboundTrunkID  string
	DispatchRuleID  string
	// Created and Reused name what Setup did, for the console.
	Created, Reused []string
}

// YAML is the tamesu.yaml snippet for the result.
func (r SetupResult) YAML() string {
	var b strings.Builder
	b.WriteString("sip:\n")
	if r.OutboundTrunkID != "" {
		fmt.Fprintf(&b, "  outbound_trunk_id: %s\n", r.OutboundTrunkID)
	}
	if r.InboundTrunkID != "" {
		fmt.Fprintf(&b, "  inbound_trunk_id: %s\n", r.InboundTrunkID)
	}
	if r.DispatchRuleID != "" {
		fmt.Fprintf(&b, "  dispatch_rule_id: %s\n", r.DispatchRuleID)
	}
	return b.String()
}

// Trunk is an existing trunk, as listed.
type Trunk struct {
	ID, Name string
	Numbers  []string
}

// Rule is an existing dispatch rule, as listed.
type Rule struct {
	ID, Name, Room string
	TrunkIDs       []string
}

// SetupAPI is the slice of the LiveKit SIP API Setup uses.
type SetupAPI interface {
	ListOutboundTrunks(ctx context.Context) ([]Trunk, error)
	CreateOutboundTrunk(ctx context.Context, name string, t TrunkConfig) (string, error)
	ListInboundTrunks(ctx context.Context) ([]Trunk, error)
	CreateInboundTrunk(ctx context.Context, name string, numbers, allowedAddresses []string) (string, error)
	ListDispatchRules(ctx context.Context) ([]Rule, error)
	CreateDispatchRule(ctx context.Context, name, room string, trunkIDs []string) (string, error)
}

// Setup creates or reuses the outbound trunk, the inbound trunk and the
// dispatch rule by name. It is idempotent: running it twice changes nothing.
func Setup(ctx context.Context, api SetupAPI, cfg SetupConfig) (SetupResult, error) {
	var res SetupResult
	if !cfg.Outbound && !cfg.Inbound {
		cfg.Outbound, cfg.Inbound = true, true
	}
	if cfg.InboundRoom == "" {
		cfg.InboundRoom = DefaultInboundRoom
	}
	if cfg.Outbound {
		if cfg.Trunk.Address == "" {
			return res, errors.New("sip setup: sip.trunk.address is required for the outbound trunk")
		}
		trunks, err := api.ListOutboundTrunks(ctx)
		if err != nil {
			return res, fmt.Errorf("sip setup: list outbound trunks: %w", err)
		}
		if t := byName(trunks, OutboundTrunkName); t != nil {
			res.OutboundTrunkID = t.ID
			res.Reused = append(res.Reused, "outbound trunk "+t.ID)
		} else {
			id, err := api.CreateOutboundTrunk(ctx, OutboundTrunkName, cfg.Trunk)
			if err != nil {
				return res, fmt.Errorf("sip setup: create outbound trunk: %w", err)
			}
			res.OutboundTrunkID = id
			res.Created = append(res.Created, "outbound trunk "+id)
		}
	}
	if cfg.Inbound {
		numbers := cfg.Trunk.InboundNumbers
		if len(numbers) == 0 {
			numbers = cfg.Trunk.Numbers
		}
		if len(numbers) == 0 {
			return res, errors.New("sip setup: sip.trunk.numbers (or inbound_numbers) is required for the inbound trunk")
		}
		trunks, err := api.ListInboundTrunks(ctx)
		if err != nil {
			return res, fmt.Errorf("sip setup: list inbound trunks: %w", err)
		}
		if t := byName(trunks, InboundTrunkName); t != nil {
			res.InboundTrunkID = t.ID
			res.Reused = append(res.Reused, "inbound trunk "+t.ID)
		} else {
			id, err := api.CreateInboundTrunk(ctx, InboundTrunkName, numbers, cfg.Trunk.AllowedAddresses)
			if err != nil {
				return res, fmt.Errorf("sip setup: create inbound trunk: %w", err)
			}
			res.InboundTrunkID = id
			res.Created = append(res.Created, "inbound trunk "+id)
		}
		rules, err := api.ListDispatchRules(ctx)
		if err != nil {
			return res, fmt.Errorf("sip setup: list dispatch rules: %w", err)
		}
		var rule *Rule
		for i := range rules {
			if rules[i].Name == DispatchRuleName {
				rule = &rules[i]
			}
		}
		if rule != nil {
			res.DispatchRuleID = rule.ID
			res.Reused = append(res.Reused, "dispatch rule "+rule.ID)
		} else {
			id, err := api.CreateDispatchRule(ctx, DispatchRuleName, cfg.InboundRoom, []string{res.InboundTrunkID})
			if err != nil {
				return res, fmt.Errorf("sip setup: create dispatch rule: %w", err)
			}
			res.DispatchRuleID = id
			res.Created = append(res.Created, "dispatch rule "+id)
		}
	}
	return res, nil
}

func byName(trunks []Trunk, name string) *Trunk {
	for i := range trunks {
		if trunks[i].Name == name {
			return &trunks[i]
		}
	}
	return nil
}

// NewSetupAPI returns the SDK-backed SetupAPI for a LiveKit server.
func NewSetupAPI(url, key, secret string) SetupAPI {
	return &sdkSetup{sip: lksdk.NewSIPClient(HTTPURL(url), key, secret)}
}

type sdkSetup struct{ sip *lksdk.SIPClient }

func (s *sdkSetup) ListOutboundTrunks(ctx context.Context) ([]Trunk, error) {
	res, err := s.sip.ListSIPOutboundTrunk(ctx, &livekit.ListSIPOutboundTrunkRequest{})
	if err != nil {
		return nil, err
	}
	var out []Trunk
	for _, t := range res.Items {
		out = append(out, Trunk{ID: t.SipTrunkId, Name: t.Name, Numbers: t.Numbers})
	}
	return out, nil
}

func (s *sdkSetup) CreateOutboundTrunk(ctx context.Context, name string, t TrunkConfig) (string, error) {
	info, err := s.sip.CreateSIPOutboundTrunk(ctx, &livekit.CreateSIPOutboundTrunkRequest{Trunk: &livekit.SIPOutboundTrunkInfo{
		Name: name, Address: t.Address, Numbers: t.Numbers, AuthUsername: t.AuthUsername, AuthPassword: t.AuthPassword,
		Transport: transport(t.Transport), Metadata: "created by tamesu sip setup",
	}})
	if err != nil {
		return "", err
	}
	return info.SipTrunkId, nil
}

func (s *sdkSetup) ListInboundTrunks(ctx context.Context) ([]Trunk, error) {
	res, err := s.sip.ListSIPInboundTrunk(ctx, &livekit.ListSIPInboundTrunkRequest{})
	if err != nil {
		return nil, err
	}
	var out []Trunk
	for _, t := range res.Items {
		out = append(out, Trunk{ID: t.SipTrunkId, Name: t.Name, Numbers: t.Numbers})
	}
	return out, nil
}

func (s *sdkSetup) CreateInboundTrunk(ctx context.Context, name string, numbers, allowed []string) (string, error) {
	info, err := s.sip.CreateSIPInboundTrunk(ctx, &livekit.CreateSIPInboundTrunkRequest{Trunk: &livekit.SIPInboundTrunkInfo{
		Name: name, Numbers: numbers, AllowedAddresses: allowed, Metadata: "created by tamesu sip setup",
	}})
	if err != nil {
		return "", err
	}
	return info.SipTrunkId, nil
}

func (s *sdkSetup) ListDispatchRules(ctx context.Context) ([]Rule, error) {
	res, err := s.sip.ListSIPDispatchRule(ctx, &livekit.ListSIPDispatchRuleRequest{})
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, r := range res.Items {
		rule := Rule{ID: r.SipDispatchRuleId, Name: r.Name, TrunkIDs: r.TrunkIds}
		if d := r.GetRule().GetDispatchRuleDirect(); d != nil {
			rule.Room = d.RoomName
		}
		out = append(out, rule)
	}
	return out, nil
}

func (s *sdkSetup) CreateDispatchRule(ctx context.Context, name, room string, trunkIDs []string) (string, error) {
	info, err := s.sip.CreateSIPDispatchRule(ctx, &livekit.CreateSIPDispatchRuleRequest{
		DispatchRule: &livekit.SIPDispatchRuleInfo{
			Name:     name,
			TrunkIds: trunkIDs,
			Metadata: "created by tamesu sip setup",
			Rule:     &livekit.SIPDispatchRule{Rule: &livekit.SIPDispatchRule_DispatchRuleDirect{DispatchRuleDirect: &livekit.SIPDispatchRuleDirect{RoomName: room}}},
		},
	})
	if err != nil {
		return "", err
	}
	return info.SipDispatchRuleId, nil
}

func transport(s string) livekit.SIPTransport {
	switch strings.ToLower(s) {
	case "udp":
		return livekit.SIPTransport_SIP_TRANSPORT_UDP
	case "tcp":
		return livekit.SIPTransport_SIP_TRANSPORT_TCP
	case "tls":
		return livekit.SIPTransport_SIP_TRANSPORT_TLS
	}
	return livekit.SIPTransport_SIP_TRANSPORT_AUTO
}
