package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Slack posts one message with the run's outcome to an incoming webhook.
type Slack struct {
	webhook      string
	onlyFailures bool
	client       *http.Client
	// Link is put at the end of the message when set (the run's report).
	Link string
}

// NewSlack returns the reporter for the webhook URL; onlyFailures skips the
// message when every test passed.
func NewSlack(webhook string, onlyFailures bool) *Slack {
	return &Slack{webhook: webhook, onlyFailures: onlyFailures, client: &http.Client{Timeout: 15 * time.Second}}
}

func (*Slack) RunStarted(RunInfo)                    {}
func (*Slack) TestStarted(*TestOutcome)              {}
func (*Slack) RunFinished(*TestOutcome, *RunOutcome) {}
func (*Slack) TestFinished(*TestOutcome)             {}

// Finish posts the message.
func (sl *Slack) Finish(ctx context.Context, s *Summary) error {
	passed, failed, errored := s.Counts()
	if sl.onlyFailures && failed == 0 && errored == 0 && (s.Comparison == nil || !s.Comparison.Regressed) {
		return nil
	}
	body, err := json.Marshal(map[string]any{"text": SlackText(s, sl.Link)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sl.webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := sl.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("slack: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	_ = passed
	return nil
}

// SlackText is the message: a headline, the failures and the baseline verdict.
func SlackText(s *Summary, link string) string {
	passed, failed, errored := s.Counts()
	icon := ":white_check_mark:"
	if failed > 0 || errored > 0 {
		icon = ":x:"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s *tamesu*: %d passed, %d failed, %d errors%s in %s", icon, passed, failed, errored, quarantinedCount(s, ", "), fmtDuration(s.Duration()))
	if s.Usage.Calls > 0 {
		fmt.Fprintf(&b, " · $%.4f", s.Usage.CostUSD)
	}
	if l := FormatLabels(s.Labels); l != "" {
		fmt.Fprintf(&b, " · %s", l)
	}
	if c := s.Comparison; c != nil {
		if c.Regressed {
			fmt.Fprintf(&b, "\n:warning: regressed against the baseline: %d new failures", len(c.NewFailures))
		} else {
			b.WriteString("\nno regression against the baseline")
		}
	}
	details := failureDetails(s)
	for i, d := range details {
		if i == 10 {
			fmt.Fprintf(&b, "\n… and %d more", len(details)-10)
			break
		}
		fmt.Fprintf(&b, "\n• *%s*%s: %s", d.test, d.quarantine, d.reason)
	}
	if link != "" {
		fmt.Fprintf(&b, "\n%s", link)
	}
	return b.String()
}
