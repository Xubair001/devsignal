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
	degradable := map[State]bool{StateEnriched: true, StateEmbedded: true}
	for _, s := range []State{
		StateDiscovered, StateFetched, StateParsed, StateNormalized,
		StateDeduped, StateEnriched, StateEmbedded,
	} {
		if got := s.Degradable(); got != degradable[s] {
			t.Errorf("%q degradable = %v, want %v", s, got, degradable[s])
		}
	}
}
