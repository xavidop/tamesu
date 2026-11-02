// Command tamesu is the tamesu CLI: agentic tests for web apps and AI agents.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Each blank import registers mamori secret providers for the schemes in
	// its comment. The core module only knows env:, file://, dotenv:// and
	// exec:; the cloud providers live here so the library modules stay free
	// of cloud SDKs (.github/scripts/check_root_deps.sh).
	_ "github.com/xavidop/mamori/providers/aws"         // aws-sm:// aws-ps:// aws-appconfig://
	_ "github.com/xavidop/mamori/providers/azure"       // azure-kv:// azure-appconfig://
	_ "github.com/xavidop/mamori/providers/doppler"     // doppler://
	_ "github.com/xavidop/mamori/providers/gcp"         // gcp-sm://
	_ "github.com/xavidop/mamori/providers/infisical"   // infisical://
	_ "github.com/xavidop/mamori/providers/k8s"         // k8s-secret:// k8s-cm://
	_ "github.com/xavidop/mamori/providers/onepassword" // op://
	_ "github.com/xavidop/mamori/providers/sops"        // sops://
	_ "github.com/xavidop/mamori/providers/vault"       // vault://

	"github.com/xavidop/tamesu/internal/cli"
)

// Set at link time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Genkit and the provider SDKs log through slog's default at Info; the CLI
	// replaces it with a redacting handler at Warn (Debug with -v) when a run
	// starts. Until then, drop it so nothing unredacted reaches the terminal.
	slog.SetDefault(slog.New(slog.DiscardHandler))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// After the first signal cancels ctx, restore the default handlers so
		// a second Ctrl-C kills a process stuck in a call that ignores ctx.
		<-ctx.Done()
		stop()
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	}()
	root := cli.NewRoot(
		cli.BuildInfo{Version: version, Commit: commit, Date: date},
		cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr},
		cli.Deps{
			LookupEnv:     os.LookupEnv,
			Now:           time.Now,
			NewProvider:   newProvider,
			Targets:       targetFactories,
			Checks:        []cli.DoctorCheck{checkModels, checkSpeech, checkChromium, checkLiveKit, checkSIP},
			SIPSetup:      sipSetup,
			RecordBrowser: recordBrowser,
			ServeMCP:      serveMCP,
		},
	)
	code := cli.Execute(ctx, root, os.Args[1:])
	stop()
	os.Exit(code)
}
