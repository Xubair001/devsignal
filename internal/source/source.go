// Package source is the ingestion boundary: the only place untrusted remote
// input enters the system.
//
// Every source is an adapter behind one interface, so adding a source is a new
// implementation plus a registry row — never a change to the platform.
package source

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Tier is the legal classification. There is deliberately no TierC constant:
// a prohibited source must not be representable, let alone storable.
type Tier string

const (
	// TierA — public ATS job-board APIs, official APIs, feeds, sitemaps.
	TierA Tier = "a"
	// TierB — permitted crawl: public, robots-allowed, no login, no accepted terms.
	TierB Tier = "b"
)

// RawDocument is exactly what the source returned, before interpretation.
// Persisting it is what makes re-parsing history possible without re-fetching.
type RawDocument struct {
	SourceJobID string
	Body        []byte
	ContentType string
	FetchedAt   time.Time
	URL         string
}

// ParsedPosting is the adapter's interpretation of one document.
//
// Fields are pointers or empty where the source did not say. An adapter must
// never invent a value to fill a field — "not disclosed" is a real answer and
// guessing is the one thing that loses a user's trust (blueprint §3).
type ParsedPosting struct {
	SourceJobID string
	ATSType     string
	ATSJobID    string

	Title       string
	CompanyName string
	// CompanyDomain is the registrable domain when the source reveals it. Empty
	// is normal; resolution then falls back to the board token.
	CompanyDomain string

	DescriptionHTML string
	ApplyURL        string
	Language        string

	// LocationRaw is kept verbatim. Structured geography is normalization's job
	// (step 8), not the adapter's — a half-parsed country is worse than none.
	LocationRaw string
	WorkMode    string // "remote" | "hybrid" | "onsite" | "" when not stated

	// SourceReportedPostedAt is THEIR claim. Stored, displayed, never scored:
	// boards and employers refresh it so listings look fresh.
	SourceReportedPostedAt *time.Time
	SourceUpdatedAt        *time.Time

	// ContentHash covers the fields that change meaning. It is the extraction
	// cache key and the exact-duplicate signal, so it must not include volatile
	// fields like a rendered timestamp.
	ContentHash []byte
}

// Cursor lets an adapter resume. For bulk-JSON sources it carries the ETag so
// the next poll can be a conditional GET.
type Cursor struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Since        time.Time `json:"since,omitempty"`
}

// ErrNotModified means the source confirmed nothing changed. This is a
// SUCCESSFUL poll: it counts for liveness and must not be treated as a failure.
var ErrNotModified = fmt.Errorf("source: not modified")

// Adapter is the whole contract.
//
// Parse MUST be pure: no network, no database, no clock. That is what makes the
// golden fixtures meaningful and what allows a whole source to be re-parsed
// after fixing a bug.
type Adapter interface {
	ID() string
	Tier() Tier
	Fetch(ctx context.Context, cur Cursor) ([]RawDocument, Cursor, error)
	Parse(doc RawDocument) ([]ParsedPosting, error)
}

// ---------------------------------------------------------------- registry

var (
	mu       sync.RWMutex
	registry = map[string]func(Options) (Adapter, error){}
)

// Options is what the platform hands an adapter at construction.
type Options struct {
	// Config is adapter-specific (for Greenhouse: the board token).
	Config map[string]string
	Client *Client
}

// Register makes an adapter constructible by name. Called from an adapter's
// init, so enabling a source family is one import.
func Register(name string, build func(Options) (Adapter, error)) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("source: duplicate registration for " + name)
	}
	registry[name] = build
}

func Build(name string, opts Options) (Adapter, error) {
	mu.RLock()
	build, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source: no adapter registered as %q", name)
	}
	return build(opts)
}

func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
