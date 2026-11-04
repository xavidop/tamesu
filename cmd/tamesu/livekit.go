package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/runner"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/livekit"
)

func init() {
	targetFactories[config.TargetLiveKit] = newLiveKitTarget
}

// newLiveKitTarget maps a suite's livekit target onto the livekit module's config.
func newLiveKitTarget(_ context.Context, spec runner.TargetSpec) (target.Target, error) {
	t := spec.Config
	cfg := livekit.Config{
		Connect: connectPolicy(t),
		URL:     t.URL, APIKey: t.APIKey, APISecret: t.APISecret, Token: t.Token, Identity: t.Identity, Room: t.Room,
		AgentIdentityPrefix: t.AgentIdentityPrefix, Mode: spec.Mode, DataTopic: t.DataTopic,
		JoinTimeout: t.JoinTimeout.Std(),
	}
	// transcript_source travels on the run context (target.Voice), set by the runner.
	if t.Dispatch != nil {
		cfg.AgentName = t.Dispatch.AgentName
	}
	return livekit.New(spec.Name, cfg), nil
}

// checkLiveKit is the `tamesu doctor` check for LiveKit: every livekit target's
// server answers over HTTP (livekit-server serves "OK" at /), with the URL
// rendered through the environment but never through secrets.
func checkLiveKit(ctx context.Context, env cli.DoctorEnv) []cli.DoctorResult {
	urls := map[string][]string{}
	for _, s := range env.Suites {
		for name, t := range s.Targets {
			if t.Type == config.TargetLiveKit && t.URL != "" {
				urls[t.URL] = append(urls[t.URL], s.Suite+"/"+name)
			}
		}
	}
	if u := env.Project.SIP.LiveKitURL; u != "" {
		urls[u] = append(urls[u], "sip.livekit_url")
	}
	if len(urls) == 0 {
		return []cli.DoctorResult{{Name: "livekit", Status: cli.StatusSkip, Detail: "no livekit target configured"}}
	}
	var out []cli.DoctorResult
	client := &http.Client{Timeout: 5 * time.Second}
	for u, users := range urls {
		httpURL := strings.Replace(strings.Replace(u, "wss://", "https://", 1), "ws://", "http://", 1)
		if strings.Contains(httpURL, "{{") {
			out = append(out, cli.DoctorResult{Name: "livekit", Status: cli.StatusWarn, Detail: fmt.Sprintf("%s: the URL is a template; checked at run time (%s)", u, strings.Join(users, ", "))})
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
		if err != nil {
			out = append(out, cli.DoctorResult{Name: "livekit", Status: cli.StatusFail, Detail: fmt.Sprintf("%s: %v", u, err)})
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			out = append(out, cli.DoctorResult{Name: "livekit", Status: cli.StatusFail, Infra: true, Detail: fmt.Sprintf("%s unreachable: %v (%s)", u, err, strings.Join(users, ", "))})
			continue
		}
		_ = resp.Body.Close()
		out = append(out, cli.DoctorResult{Name: "livekit", Status: cli.StatusOK, Detail: fmt.Sprintf("%s answers HTTP %d (%s)", u, resp.StatusCode, strings.Join(users, ", "))})
	}
	return out
}
