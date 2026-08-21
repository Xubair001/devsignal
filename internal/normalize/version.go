// Package normalize derives structured fields from the free text a source gave
// us. Everything here is deterministic and pure — no network, no clock, no
// database — so the whole corpus can be re-normalized after a ruleset change.
//
// The governing rule is blueprint §3: assert only what the text states. A field
// left NULL is an honest "the source did not say"; a guessed field is a claim we
// cannot defend, and one invented field discredits the ones next to it.
package normalize

// Version identifies this ruleset. Bump it when a rule changes, so re-normalized
// rows are distinguishable from stale ones and a backfill can be targeted.
const Version = "norm-2026-08-21"
