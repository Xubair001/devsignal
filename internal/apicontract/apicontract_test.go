// Package apicontract asserts that the JSON field names the web console reads
// still exist on the Go response types.
//
// The console's TypeScript DTOs are hand-written at the boundary, which is the
// right call — a generated client would hide the awkward parts the display
// rules depend on, like `salary: null` being a real state. The cost is that a
// renamed json tag is invisible to both compilers: Go still builds, `tsc` still
// passes, and the field silently arrives as undefined in the browser.
//
// This is the cheapest possible guard for that. It walks the real structs by
// reflection, needs no database and no running server, so it runs on every
// `make test` rather than only when someone remembers to check by hand.
//
// It deliberately does NOT assert the whole shape. Adding a field is fine and
// should not fail; only removing or renaming one the console reads is a break.
package apicontract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Xubair001/devsignal/internal/engagement"
	"github.com/Xubair001/devsignal/internal/opportunity"
)

// consumed lists every json path the console reads, per response type.
//
// Dotted paths walk into nested structs and through slices, so
// "items.posting.liveness.verified_open" checks the field a feed card needs to
// make the product's central claim.
var consumed = map[string][]struct {
	typ  reflect.Type
	path string
}{}

func paths(t reflect.Type, ps ...string) []struct {
	typ  reflect.Type
	path string
} {
	out := make([]struct {
		typ  reflect.Type
		path string
	}, 0, len(ps))
	for _, p := range ps {
		out = append(out, struct {
			typ  reflect.Type
			path string
		}{t, p})
	}
	return out
}

func init() {
	consumed["feed"] = paths(reflect.TypeOf(engagement.FeedResponse{}),
		"diagnostics.eligible_after_predicates",
		"diagnostics.retrieved",
		"diagnostics.passed_eligibility_gate",
		"diagnostics.excluded_by_gate",
		"diagnostics.retrieval_truncated",
		"diagnostics.closed_since_scoring",
		"items.opportunity_id",
		"items.title",
		"items.channels",
		"items.state.saved",
		"items.state.applied",
		"items.state.applied_at",
		"items.state.dismissed",
		"items.fit.band",
		"items.fit.points",
		"items.fit.max_points",
		"items.fit.summary",
		"items.fit.factors.factor",
		"items.fit.factors.points",
		"items.fit.factors.max_points",
		// `scored` is what separates "we could not read this" from "you match
		// none of it". The ledger renders those two differently and must not
		// have to guess which it is looking at.
		"items.fit.factors.scored",
		"items.fit.versions.weights",
		// The posting. Liveness in particular: the display rules forbid showing
		// a role in the daily feed whose open state is unknown.
		"items.posting.company.name",
		"items.posting.company.domain_confirmed",
		"items.posting.location.city",
		"items.posting.location.country",
		"items.posting.location.work_mode",
		"items.posting.location.geo_scope",
		"items.posting.role.family",
		"items.posting.role.seniority",
		"items.posting.salary",
		"items.posting.apply_url",
		"items.posting.liveness.verified_open",
		"items.posting.liveness.checked_at",
		"items.posting.liveness.days_open",
		"items.posting.signals.ghost_risk",
		"items.posting.signals.ghost_risk_reasons",
	)

	consumed["excluded"] = paths(reflect.TypeOf(engagement.ExcludedResponse{}),
		"items.opportunity_id", "items.title", "items.failed_checks", "items.reasons",
	)

	// The corpus browser and the detail page.
	consumed["opportunity"] = paths(reflect.TypeOf(opportunity.Detail{}),
		"id", "title", "company.name", "company.domain_confirmed",
		"role.family", "role.seniority", "role.is_management",
		"location.city", "location.country", "location.work_mode", "location.geo_scope",
		"salary", "apply_url", "language", "visa_sponsorship",
		"liveness.verified_open", "liveness.checked_at", "liveness.first_seen_at",
		"liveness.days_open", "liveness.source_claims_posted_at",
		"signals.ghost_risk", "signals.ghost_risk_reasons",
		"signals.times_refreshed", "signals.sources_seen_on",
		// Sanitized server-side before it reaches a client. The console renders it
		// as HTML, so a rename here would either blank the description or, worse,
		// point at a field that was never filtered.
		"description_html", "open_similar_roles_at_company",
	)

	consumed["page"] = paths(reflect.TypeOf(opportunity.Page{}),
		"items.id", "items.title", "next_cursor",
	)

	consumed["money"] = paths(reflect.TypeOf(opportunity.Money{}),
		// Minor units and the currency, never a formatted string: formatting
		// depends on a locale the API does not know.
		"min_minor", "max_minor", "currency", "period", "is_estimated",
	)
}

func TestConsoleReadsFieldsThatExist(t *testing.T) {
	for surface, entries := range consumed {
		for _, e := range entries {
			if _, err := resolve(e.typ, e.path); err != nil {
				t.Errorf("%s: console reads %q but %v", surface, e.path, err)
			}
		}
	}
}

// TestFeedPostingIsNotOptional guards the one field whose absence is a product
// bug rather than a missing detail.
//
// A pointer or an omitempty on the posting would let an item reach the client
// with no liveness at all, which is exactly what the daily feed may not show.
// The handler drops such an item instead — that is the contract, and it is only
// enforceable if the field cannot be null.
func TestFeedPostingIsNotOptional(t *testing.T) {
	f, err := field(reflect.TypeOf(engagement.FeedItem{}), "posting")
	if err != nil {
		t.Fatalf("FeedItem has no posting: a feed card cannot show liveness (%v)", err)
	}
	if f.Type.Kind() == reflect.Pointer {
		t.Error("posting is a pointer; an item without one must be dropped, not nulled")
	}
	if strings.Contains(f.Tag.Get("json"), "omitempty") {
		t.Error("posting is omitempty, so liveness can vanish from a rendered card")
	}
}

// resolve walks a dotted json path, stepping through slices and pointers.
func resolve(t reflect.Type, path string) (reflect.StructField, error) {
	var last reflect.StructField
	cur := t
	for _, seg := range strings.Split(path, ".") {
		f, err := field(cur, seg)
		if err != nil {
			return last, err
		}
		last, cur = f, deref(f.Type)
	}
	return last, nil
}

func field(t reflect.Type, tag string) (reflect.StructField, error) {
	t = deref(t)
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, &missingErr{tag, t.String()}
	}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == tag {
			return f, nil
		}
		// Embedded structs contribute their fields inline, the way Detail
		// embeds Summary.
		if f.Anonymous {
			if inner, err := field(f.Type, tag); err == nil {
				return inner, nil
			}
		}
	}
	return reflect.StructField{}, &missingErr{tag, t.String()}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t
}

type missingErr struct{ tag, on string }

func (e *missingErr) Error() string {
	return "no field with json tag \"" + e.tag + "\" on " + e.on
}
