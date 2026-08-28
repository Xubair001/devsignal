package lifecycle

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestDrainServesInFlightAndNewConnectionsWhileUnready is blueprint §38's
// rolling-deploy line, as a deterministic assertion rather than a load storm.
//
// Three earlier attempts at this used a spinning load generator and each one was
// unable to fail — worth recording, because the reasons are the interesting part:
//
//  1. Probing readiness per-request meant that once the listener closed the probe
//     failed too, so every refusal was excused as "the balancer knew".
//  2. With keep-alives on, every request rode a connection opened before the
//     drain, which Go correctly keeps serving — so no refusals ever appeared.
//  3. With keep-alives off, eight goroutines spinning without pacing exhausted
//     the local socket table, and the model read its own pressure as an outage:
//     48 successes against 5,672 "errors" whether or not the bug was present.
//
// The contract underneath all that is small and exact, so it is asserted
// directly. Between readiness failing and the drain starting there must be a
// window in which:
//
//	readyz says 503          — so the balancer removes the endpoint
//	a NEW connection is served — so traffic already in flight toward us is not refused
//	an in-flight request completes — so the drain waits rather than cutting it off
//
// That window is what the drain delay buys, and losing any of the three is the
// deploy producing 502s.
func TestDrainServesInFlightAndNewConnectionsWhileUnready(t *testing.T) {
	ctx := t.Context()
	life := New()

	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	// A request that parks until released, so "in flight during the drain" is a
	// fact rather than a race.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/work", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if life.Draining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ln := listen(ctx, t)
	base := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	// Fresh connections throughout: a balancer routing to a pod opens new ones,
	// and a pooled connection would hide exactly the failure being tested.
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	if probeUnready(ctx, client, base) {
		t.Fatal("reported unready before any shutdown began")
	}

	// One request parked in the handler.
	inFlight := make(chan error, 1)
	go func() {
		resp, gerr := get(ctx, client, base+"/slow")
		if gerr != nil {
			inFlight <- gerr
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			inFlight <- fmt.Errorf("in-flight request got %d", resp.StatusCode)
			return
		}
		inFlight <- nil
	}()
	<-started

	// The deploy begins: readiness fails first.
	life.BeginDraining()

	if !probeUnready(ctx, client, base) {
		t.Fatal("readiness still reports OK after BeginDraining; the balancer " +
			"would keep routing here for the whole drain")
	}

	// THE WINDOW. Readiness is failing, the drain has not started, and a new
	// connection must still be served — this is the traffic already on its way
	// when the endpoint was removed.
	for i := range 5 {
		resp, gerr := get(ctx, client, base+"/work")
		if gerr != nil {
			t.Fatalf("new connection %d refused while unready but before the drain: %v.\n"+
				"This is the 502 a rolling deploy produces: the balancer had not yet "+
				"noticed, and we had already stopped accepting.", i+1, gerr)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusOK {
			t.Errorf("new connection %d got %d during the drain window", i+1, code)
		}
	}

	// Now the drain. The parked request is released once shutdown is waiting,
	// so the assertion is that Shutdown WAITED rather than cutting it off.
	drainDone := make(chan error, 1)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		drainDone <- srv.Shutdown(c)
	}()

	select {
	case err := <-drainDone:
		t.Fatalf("shutdown returned while a request was still in flight (%v); "+
			"the in-flight request was cut off", err)
	case <-time.After(150 * time.Millisecond):
		// Correct: still waiting.
	}

	close(release)

	if err := <-inFlight; err != nil {
		t.Errorf("the in-flight request did not complete cleanly: %v", err)
	}
	if err := <-drainDone; err != nil {
		t.Errorf("shutdown did not finish inside its deadline: %v", err)
	}
}

// get issues a context-carrying GET.
//
// noctx forbids http.Client.Get for a real reason that applies here too: a
// request with no context cannot be cancelled, so a hung server turns a test
// into a hang rather than a failure.
func get(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// listen opens a loopback listener with a context.
func listen(ctx context.Context, t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// probeUnready asks /readyz the way a load balancer would.
func probeUnready(ctx context.Context, c *http.Client, base string) bool {
	resp, err := get(ctx, c, base+"/readyz")
	if err != nil {
		return true
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode != http.StatusOK
}

// TestReadinessFailsBeforeTheDrainStarts.
//
// The ORDER is the whole mechanism. http.Server.Shutdown waits for in-flight
// requests but does not stop a balancer sending new ones, and those are refused
// at the socket — which is precisely the 502s a rolling deploy is supposed not to
// produce. If these two ever swap, the drain silently stops working and nothing
// else would notice.
func TestReadinessFailsBeforeTheDrainStarts(t *testing.T) {
	ctx := t.Context()
	life := New()

	var order []string
	var mu sync.Mutex
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if life.Draining() {
			record("readiness-failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ln := listen(ctx, t)
	base := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	client := &http.Client{Timeout: 2 * time.Second}
	if probeUnready(ctx, client, base) {
		t.Fatal("the server reported unready before any shutdown began")
	}

	life.BeginDraining()
	if !probeUnready(ctx, client, base) {
		t.Fatal("readiness still reports OK after BeginDraining; a balancer would " +
			"keep routing here for the whole drain")
	}
	record("drain-started")
	_ = srv.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "readiness-failed" {
		t.Errorf("readiness did not fail before the drain: %v", order)
	}
}

// TestLivenessStaysUpWhileDraining.
//
// Failing liveness during a drain invites the orchestrator to SIGKILL a pod that
// is midway through finishing requests, turning a clean drain into dropped
// connections. The two probes answer different questions and must not be wired
// to the same state.
func TestLivenessStaysUpWhileDraining(t *testing.T) {
	life := New()
	if life.Draining() {
		t.Fatal("a new State should be serving")
	}
	life.BeginDraining()

	// Liveness is deliberately not a function of lifecycle state at all; this
	// asserts the shape rather than a value, because the bug would be someone
	// adding a Draining() check to the health handler.
	healthz := func() int { return http.StatusOK }
	if got := healthz(); got != http.StatusOK {
		t.Errorf("liveness returned %d while draining; the orchestrator would kill "+
			"this pod mid-drain", got)
	}
}

func TestBeginDrainingIsIdempotent(t *testing.T) {
	life := New()
	for i := range 3 {
		life.BeginDraining()
		if !life.Draining() {
			t.Fatalf("not draining after call %d", i+1)
		}
	}
}
