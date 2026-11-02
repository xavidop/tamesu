---
layout: ../../layouts/DocsLayout.astro
title: Go API
---

# Go API

Everything the CLI does is available from Go, so `go test` alone can drive agent tests. The
root module `github.com/xavidop/tamesu` holds the test, assertion and report types; the AI
client and the targets are modules you import when you need them, such as
`github.com/xavidop/tamesu/llm` and `github.com/xavidop/tamesu/targets/rest`.

## Run a test from `go test`

```go
package support_test

import (
    "context"
    "testing"

    "github.com/xavidop/tamesu"
    "github.com/xavidop/tamesu/assert"
    "github.com/xavidop/tamesu/llm"
    "github.com/xavidop/tamesu/targets/rest"
)

func TestRefundPolicy(t *testing.T) {
    client, err := llm.New(context.Background(), llm.Config{
        Plugins:     []string{"googlegenai"},
        Models:      llm.Models{Default: "googleai/gemini-2.5-flash"},
        Credentials: func(name string) (string, bool) { return tamesu.Env(name), tamesu.Env(name) != "" },
    })
    if err != nil {
        t.Fatal(err)
    }
    api := rest.New("api", rest.Config{
        URL:      tamesu.Env("AGENT_URL"),
        Headers:  map[string]string{"Authorization": "Bearer {{secrets.API_TOKEN}}"},
        TextPath: "$.reply", ToolCallsPath: "$.tool_calls[*]", DonePath: "$.end_session",
    })
    tamesu.Run(t, tamesu.Test{
        Name:     "refund policy question",
        Target:   api,
        Persona:  tamesu.Persona{Name: "Marta", Style: "polite, slightly confused"},
        Scenario: tamesu.Scenario{Goal: "Find out if you can return shoes bought 40 days ago", MaxTurns: 8},
        Repeat:   3, PassThreshold: 0.66,
        Assert: []assert.Assertion{
            llm.Judge("The agent states the 30-day return policy and does not promise an exception"),
            assert.NotContains("I'm just an AI"),
            assert.ToolCalled("lookup_policy", nil),
            assert.Metric("turn_latency_ms_p95").Lt(4000),
        },
    },
        tamesu.WithProvider(client),
        tamesu.WithSecrets(map[string]string{"API_TOKEN": "env:API_TOKEN"}),
    )
}
```

`tamesu.Run` makes a subtest per repeat, logs every turn with `t.Log`, fails the test when the
pass threshold is not met, and writes the run directory (`summary.json`, `junit.xml`,
`tests/<slug>/run-<n>/`) to `$TAMESU_OUT` or the test's temp dir. It returns the
`*report.TestOutcome` for further checks.

Options: `WithProvider` (the AI layer, an `*llm.Client`), `WithSecrets` (mamori refs, resolved
once), `WithMamoriOptions` (ref vars, providers, an env file), `WithVars`, `WithOutDir`,
`WithReporter`, `WithProject`.

## Load a YAML suite

```go
suite, err := tamesu.LoadSuite("tests/support.yaml")   // validated; the error lists every file:line problem
```

## Assertions

The `assert` package holds the built-ins: `Contains`, `NotContains`, `Regex`, `JSONPath`,
`Status`, `ToolCalled`, `ToolNotCalled`, `Metric(name).Lt(...)`, `Turns`, `Judge`,
`Evaluate`, `OnTurn`, and the sip ones `DTMFReceived`, `CallEndedBy`, `TransferredTo`. Write
your own with `assert.Func`, or implement the interface:

```go
type Assertion interface {
    Name() string
    Evaluate(ctx context.Context, r *assert.RunResult) assert.Result
}
```

`assert.RunResult` has the transcript, tool calls, metrics, raw responses, statuses, events and
the `Judger`/`Evaluator` hooks. `assert.Register("name", factory)` makes a Go assertion
available to YAML suites as `custom: name`.

## Targets

```go
type Target interface {
    Name() string
    Start(ctx context.Context, run *target.RunContext) (target.Session, error)
}

type Session interface {
    Send(ctx context.Context, turn target.UserTurn) error
    Receive(ctx context.Context) (target.AgentTurn, error)
    Close(ctx context.Context) (target.Artifacts, error)
}
```

A target renders its strings through `run.Render` (vars, secrets, `{{uuid}}`), reports the
transport timestamps in `AgentTurn.StartedAt`/`FirstTokenAt`/`EndedAt`, and returns metrics,
files and timeline events from `Close`. `github.com/xavidop/tamesu/targettest` checks a target
of your own against that contract; see [Contributing](/docs/contributing/).

## The AI layer

`llm.New(ctx, llm.Config{...})` returns the client the runner uses: `Simulate`, `Judge`,
`Evaluate`, `Usage`. `llm.WithGenkit(g)` reuses an existing Genkit instance. `llm.Judge` is the
judge assertion, so a Go test reads naturally. See [AI](/docs/ai/).
