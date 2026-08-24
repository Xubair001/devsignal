// Package pipeline is the work queue and state machine every worker plugs into.
//
// The database row is the truth. Events, when they exist, are a latency
// optimization: losing one costs seconds, never data. That is why every stage
// claims by state, advances in the same transaction as its work, and why a
// sweeper re-enqueues anything stranded (see the pipeline-stage skill).
package pipeline

import "fmt"

type State string

const (
	StateDiscovered      State = "discovered"
	StateFetched         State = "fetched"
	StateParsed          State = "parsed"
	StateNormalized      State = "normalized"
	StateDeduped         State = "deduped"
	StateEnriched        State = "enriched"
	StateEmbedded        State = "embedded"
	StateReady           State = "ready"
	StateFailedPermanent State = "failed_permanent"
)

// happyPath is the ordinary progression. It is declared once so a stage cannot
// invent a transition, and so the sweeper knows what is non-terminal.
var happyPath = map[State]State{
	StateDiscovered: StateFetched,
	StateFetched:    StateParsed,
	StateParsed:     StateNormalized,
	StateNormalized: StateDeduped,
	StateDeduped:    StateEnriched,
	StateEnriched:   StateEmbedded,
	StateEmbedded:   StateReady,
}

// Next returns the successor of s.
func Next(s State) (State, error) {
	n, ok := happyPath[s]
	if !ok {
		return "", fmt.Errorf("pipeline: %q has no successor", s)
	}
	return n, nil
}

// Terminal states are never claimed and never swept.
func (s State) Terminal() bool {
	return s == StateReady || s == StateFailedPermanent
}

// Degradable reports whether a failure of the work done IN this state may still
// be published with a quality flag rather than blocking the record.
//
// Note carefully which state this is about. A worker claims an item IN state X
// and runs the handler that advances it OUT of X, so the state names the work
// still to be done:
//
//	deduped  -> the enrichment handler runs   (optional: external model)
//	enriched -> the embedding handler runs    (optional: external model)
//
// So these two are the degradable ones. An earlier version listed enriched and
// embedded, which is off by one: a permanently failing enrichment would park in
// failed_permanent and never reach 'ready'. That is exactly the failure this rule
// exists to prevent — a strictly sequential chain making the least reliable
// dependency a hard prerequisite for a posting ever becoming visible.
//
// The stages that establish identity and correctness never degrade.
func (s State) Degradable() bool {
	return s == StateDeduped || s == StateEnriched
}

// AllNonTerminal is used by the stats/sweeper views.
func AllNonTerminal() []State {
	out := make([]State, 0, len(happyPath))
	for s := range happyPath {
		out = append(out, s)
	}
	return out
}
