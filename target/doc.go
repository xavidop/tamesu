// Package target defines the contract a system under test implements: a
// Target that starts a Session (conversational targets: rest, livekit, sip)
// or a BrowserSession (the browser target), plus the turn, artifact and
// run-context types they exchange with the runner. Concrete targets live in
// the targets/* modules so their SDKs never reach the core.
package target
