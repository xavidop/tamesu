// Package cli is the tamesu command tree. It is built from injected build
// info, streams and dependencies and never reads os.Args or calls os.Exit, so
// every command is testable in-process; cmd/tamesu wires the real ones.
package cli
