package pipeline

import "testing"

func TestHappyPathReachesReady(t *testing.T) {
	s := StateDiscovered
	steps := 0
	for !s.Terminal() {
		next, err := Next(s)
		if err != nil {
			t.Fatalf("no successor for %q after %d steps", s, steps)
		}
		s = next
		steps++
		if steps > 20 {
			t.Fatal("cycle in the state machine")
		}
	}
	if s != StateReady {
		t.Fatalf("ended at %q, want ready", s)
	}
	if steps != 7 {
		t.Fatalf("happy path is %d steps, want 7", steps)
	}
}

func TestTerminalStatesHaveNoSuccessor(t *testing.T) {
	for _, s := range []State{StateReady, StateFailedPermanent} {
		if !s.Terminal() {
			t.Errorf("%q should be terminal", s)
		}
		if _, err := Next(s); err == nil {
			t.Errorf("%q should have no successor", s)
		}
	}
}

func TestOnlyEnrichmentStagesDegrade(t *testing.T) {
	// A posting with no extracted skills is still better than an invisible one;
	// a posting with no identity is not.
	//
	// The state names the work STILL TO DO, because a worker claims an item in
	// state X and runs the handler that advances it out of X. Enrichment runs on
	// 'deduped' and embedding runs on 'enriched', so those are the degradable
	// pair. Getting this off by one parks a failing enrichment in
	// failed_permanent instead of publishing it degraded.
	degradable := map[State]bool{StateDeduped: true, StateEnriched: true}
	for _, s := range []State{
		StateDiscovered, StateFetched, StateParsed, StateNormalized,
		StateDeduped, StateEnriched, StateEmbedded,
	} {
		if got := s.Degradable(); got != degradable[s] {
			t.Errorf("%q degradable = %v, want %v", s, got, degradable[s])
		}
	}
}

// Every state whose handler calls an external model must be degradable, or an
// outage at that provider silently stops postings becoming visible.
func TestStatesWithExternalDependenciesAreDegradable(t *testing.T) {
	// enrichment runs on deduped; embeddings run on enriched.
	for _, s := range []State{StateDeduped, StateEnriched} {
		if !s.Degradable() {
			t.Errorf("%q runs an external-model handler but is not degradable: a provider "+
				"outage would block visibility", s)
		}
	}
	// Identity and correctness stages must NOT degrade: publishing a posting with
	// no company or no dedup decision is worse than not publishing it.
	for _, s := range []State{StateDiscovered, StateFetched, StateParsed, StateNormalized} {
		if s.Degradable() {
			t.Errorf("%q establishes identity or correctness and must not degrade", s)
		}
	}
}
