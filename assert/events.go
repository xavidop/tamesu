package assert

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Event kinds the voice targets emit on the session timeline. The sip
// assertions read them.
const (
	EventDTMFReceived = "dtmf_received" // Detail: the digits the agent sent
	EventCallEnded    = "call_ended"    // Detail: agent, caller, timeout, error, no_answer or busy
	EventTransfer     = "transfer"      // Detail: the SIP REFER target
)

// DTMFReceived passes when the digits the agent sent, concatenated, equal digits.
func DTMFReceived(digits string) Assertion {
	return Func("dtmf_received "+digits, func(_ context.Context, r *RunResult) Result {
		var got strings.Builder
		for _, e := range r.Events {
			if e.Kind == EventDTMFReceived {
				got.WriteString(e.Detail)
			}
		}
		if got.Len() == 0 {
			return Result{Pass: false, Reason: "the agent sent no DTMF digits"}
		}
		return Result{Pass: got.String() == digits, Reason: fmt.Sprintf("the agent sent %q, want %q", got.String(), digits)}
	})
}

// CallEndedBy passes when the call ended for one of the listed reasons
// (agent, caller, timeout, error, no_answer, busy).
func CallEndedBy(who []string) Assertion {
	return Func("call_ended_by "+strings.Join(who, "|"), func(_ context.Context, r *RunResult) Result {
		for _, e := range r.Events {
			if e.Kind == EventCallEnded {
				return Result{Pass: slices.Contains(who, e.Detail), Reason: fmt.Sprintf("the call was ended by %s, want %s", e.Detail, strings.Join(who, " or "))}
			}
		}
		return Result{Pass: false, Reason: "the call did not end (no call_ended event)"}
	})
}

// TransferredTo passes when the agent transferred the call (a SIP REFER) to a
// target matching pattern.
func TransferredTo(pattern string) (Assertion, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("transferred_to %q: %w", pattern, err)
	}
	return Func("transferred_to "+quote(pattern), func(_ context.Context, r *RunResult) Result {
		var targets []string
		for _, e := range r.Events {
			if e.Kind == EventTransfer {
				targets = append(targets, e.Detail)
				if re.MatchString(e.Detail) {
					return Result{Pass: true, Reason: fmt.Sprintf("the call was transferred to %s", e.Detail)}
				}
			}
		}
		if len(targets) == 0 {
			return Result{Pass: false, Reason: "the call was not transferred"}
		}
		return Result{Pass: false, Reason: fmt.Sprintf("the call was transferred to %s, which does not match %s", strings.Join(targets, ", "), quote(pattern))}
	}), nil
}
