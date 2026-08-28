// Package lifecycle carries the process's serving state.
//
// It exists for one line of blueprint §38: "A rolling deploy produces zero 5xx
// and zero lost jobs, verified under load."
//
// Draining an HTTP server is not enough on its own. http.Server.Shutdown stops
// accepting new connections and waits for in-flight ones, which handles the
// requests already arriving — but a load balancer that still believes the pod is
// healthy keeps SENDING new ones, and those are refused at the socket. The
// result is exactly the 502s a rolling deploy is supposed not to produce.
//
// The fix is that readiness has to fail BEFORE the drain begins, so the balancer
// removes the endpoint while the server is still serving. That is a state the
// probe handler and the shutdown path both need, which is what this package is.
package lifecycle

import "sync/atomic"

// State is the process's willingness to accept new traffic.
//
// Deliberately separate from health. Liveness answers "is this process working"
// and must not fail during a drain — failing it invites the orchestrator to
// SIGKILL a pod that is in the middle of finishing requests, which turns a clean
// drain into dropped connections.
type State struct {
	draining atomic.Bool
}

// New returns a State that is serving.
func New() *State { return &State{} }

// BeginDraining marks the process as no longer accepting new traffic.
//
// Called on the shutdown signal, before http.Server.Shutdown, and the gap
// between the two is what the balancer needs to notice.
func (s *State) BeginDraining() { s.draining.Store(true) }

// Draining reports whether shutdown has begun.
func (s *State) Draining() bool { return s.draining.Load() }
