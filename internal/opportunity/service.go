package opportunity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/ghostrisk"
	"github.com/Xubair001/devsignal/internal/store"
)

var (
	ErrNotFound     = errors.New("opportunity not found")
	ErrInvalidInput = errors.New("invalid input")
)

// MaxPageSize bounds what a client can ask for. The product shows a handful of
// items, so a large page is either a mistake or someone enumerating the corpus.
const (
	DefaultPageSize = 25
	MaxPageSize     = 100
)

// Clock is injected: liveness and ghost risk are time-dependent, so a real clock
// would make them untestable.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Service struct {
	q     *store.Queries
	clock Clock
}

func NewService(pool *pgxpool.Pool, c Clock) *Service {
	if c == nil {
		c = realClock{}
	}
	return &Service{q: store.New(pool), clock: c}
}

type ListFilter struct {
	RoleFamily *string
	WorkMode   *string
	Country    *string
	PageSize   int
	Cursor     string
}

// SummariesByID returns the read-side summary for a specific set of ids,
// keyed by id.
//
// This exists so the feed can render a card without a second DTO. The feed
// knows which postings it selected and in what order; what it does not have is
// the company, salary, apply URL and liveness, and liveness in particular is
// not optional decoration — the display rules forbid showing a posting in the
// daily feed whose open state is unknown. Sharing Summary means a field added
// for the browse list cannot silently go missing here.
//
// Serving filters still apply: a posting closed or merged between scoring and
// render is absent from the map, and the caller drops it rather than showing a
// dead role.
func (s *Service) SummariesByID(
	ctx context.Context, ids []pgtype.UUID,
) (map[string]Summary, error) {
	if len(ids) == 0 {
		return map[string]Summary{}, nil
	}
	rows, err := s.q.ListOpportunities(ctx, store.ListOpportunitiesParams{
		Ids: ids,
		// The id list is the bound. PageSize is still required by the query, so
		// it is set to the number asked for rather than a page default that
		// would silently truncate the feed.
		PageSize: int32(len(ids)),
	})
	if err != nil {
		return nil, fmt.Errorf("summaries by id: %w", err)
	}
	now := s.clock.Now()
	out := make(map[string]Summary, len(rows))
	for _, r := range rows {
		out[r.ID.String()] = s.summaryFromList(r, now)
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, f ListFilter) (*Page, error) {
	size := f.PageSize
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}

	afterSeen, afterID, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}

	rows, err := s.q.ListOpportunities(ctx, store.ListOpportunitiesParams{
		RoleFamily: f.RoleFamily,
		WorkMode:   f.WorkMode,
		Country:    f.Country,
		AfterSeen:  afterSeen,
		AfterID:    afterID,
		PageSize:   int32(size),
	})
	if err != nil {
		return nil, fmt.Errorf("list opportunities: %w", err)
	}

	now := s.clock.Now()
	page := &Page{Items: make([]Summary, 0, len(rows))}
	for _, r := range rows {
		page.Items = append(page.Items, s.summaryFromList(r, now))
	}
	// Only emit a cursor when the page was full: a short page is the end, and
	// handing out a cursor there makes clients issue a pointless extra request.
	if len(rows) == size && size > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(last.FirstSeenAt.Time, last.ID)
	}
	return page, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Detail, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	r, err := s.q.GetOpportunityDetail(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get opportunity: %w", err)
	}

	now := s.clock.Now()

	// The company's own hiring pace is the ghost-risk baseline. A failure here
	// must degrade to "no baseline", never fail the request: an unknown baseline
	// simply contributes nothing.
	var median int32
	if m, merr := s.q.CompanyMedianDaysToClose(ctx, r.CompanyID); merr == nil {
		median = m
	}

	sum := Summary{
		ID:      r.ID.String(),
		Title:   r.TitleRaw,
		Company: Company{Name: r.CompanyName, DomainConfirmed: r.DomainConfirmed},
		Role: Role{
			Family: r.RoleFamily, Seniority: seniorityLabel(r.SeniorityOrdinal),
			IsManagement: r.IsManagement,
		},
		Location: Location{
			Country: r.LocationCountry, City: r.LocationCity,
			WorkMode: r.WorkMode, GeoScope: splitScope(r.RemoteGeoScope),
		},
		Salary:          money(r.SalaryMinMinor, r.SalaryMaxMinor, r.SalaryCurrency, r.SalaryPeriod, r.SalaryIsEstimated),
		VisaSponsorship: r.VisaSponsorship,
		Language:        r.Language,
		ApplyURL:        r.ApplyUrl,
		Liveness: liveness(r.ClosedAt, r.LivenessCheckedAt, r.FirstSeenAt,
			r.SourceReportedPostedAt, now),
		Signals: signals(r.RepostCount, int(r.SourceCount), r.ApplyMethod,
			r.FirstSeenAt, r.LastSeenAt, median, now),
	}

	var similar int64
	if r.RoleFamily != nil {
		if n, cerr := s.q.CountOpenSimilarRoles(ctx, store.CountOpenSimilarRolesParams{
			CompanyID: r.CompanyID, RoleFamily: r.RoleFamily,
		}); cerr == nil {
			similar = n
		}
	}

	return &Detail{
		Summary: sum,
		// Sanitized here, at the boundary where it becomes a client's problem.
		// The stored column holds the board's bytes verbatim; see sanitize.go.
		DescriptionHTML:           SanitizeDescription(r.DescriptionText),
		OpenSimilarRolesAtCompany: int(similar),
	}, nil
}

func (s *Service) summaryFromList(r store.ListOpportunitiesRow, now time.Time) Summary {
	return Summary{
		ID:      r.ID.String(),
		Title:   r.TitleRaw,
		Company: Company{Name: r.CompanyName, DomainConfirmed: r.DomainConfirmed},
		Role: Role{
			Family: r.RoleFamily, Seniority: seniorityLabel(r.SeniorityOrdinal),
			IsManagement: r.IsManagement,
		},
		Location: Location{
			Country: r.LocationCountry, City: r.LocationCity,
			WorkMode: r.WorkMode, GeoScope: splitScope(r.RemoteGeoScope),
		},
		Salary:          money(r.SalaryMinMinor, r.SalaryMaxMinor, r.SalaryCurrency, r.SalaryPeriod, r.SalaryIsEstimated),
		VisaSponsorship: r.VisaSponsorship,
		Language:        r.Language,
		ApplyURL:        r.ApplyUrl,
		// The list query does not join company history: the per-company baseline
		// costs a query per row. List therefore assesses without it, which can
		// only ever be more conservative, not less.
		// The list query already excludes closed rows, so verified-open is implied.
		Liveness: liveness(pgtype.Timestamptz{}, r.LivenessCheckedAt, r.FirstSeenAt,
			r.SourceReportedPostedAt, now),
		Signals: signals(r.RepostCount, int(r.SourceCount), r.ApplyMethod,
			r.FirstSeenAt, r.LastSeenAt, 0, now),
	}
}

// ---------------------------------------------------------------- mapping

func liveness(closedAt, checkedAt, firstSeen, theirPosted pgtype.Timestamptz, now time.Time) Liveness {
	l := Liveness{
		VerifiedOpen: !closedAt.Valid,
		FirstSeenAt:  firstSeen.Time,
		DaysOpen:     ghostrisk.DaysOpen(firstSeen.Time, now),
	}
	if checkedAt.Valid {
		t := checkedAt.Time
		l.CheckedAt = &t
	}
	if theirPosted.Valid {
		t := theirPosted.Time
		l.SourceClaimsPostedAt = &t
	}
	return l
}

func signals(repostCount int32, sourceCount int, applyMethod *string,
	firstSeen, lastSeen pgtype.Timestamptz, medianDays int32, now time.Time) Signals {
	a := ghostrisk.Assess(ghostrisk.Signals{
		FirstSeenAt:              firstSeen.Time,
		LastVerifiedAt:           lastSeen.Time,
		RepostCount:              int(repostCount),
		HasApplyMethod:           applyMethod != nil && *applyMethod != "",
		CompanyMedianDaysToClose: int(medianDays),
	}, now)
	reasons := a.Reasons
	if reasons == nil {
		// [] rather than null: a client calling .length on null breaks, and an
		// empty list is the accurate answer.
		reasons = []ghostrisk.Reason{}
	}
	return Signals{
		GhostRisk:        a.Band,
		GhostRiskReasons: reasons,
		TimesRefreshed:   int(repostCount),
		SourcesSeenOn:    sourceCount,
		ApplyMethod:      applyMethod,
	}
}

// money returns nil when nothing was disclosed. Callers must treat nil as its
// own state; defaulting it to a placeholder is the invented field §3 forbids.
func money(min, max *int64, currency, period *string, estimated bool) *Money {
	if min == nil && max == nil {
		return nil
	}
	m := &Money{IsEstimated: estimated}
	if min != nil {
		m.MinMinor = *min
	}
	m.MaxMinor = max
	if currency != nil {
		m.Currency = *currency
	}
	if period != nil {
		m.Period = *period
	}
	return m
}

func splitScope(scope *string) []string {
	if scope == nil || *scope == "" {
		return nil
	}
	return strings.Split(*scope, ",")
}

// ---------------------------------------------------------------- cursor

// The cursor is opaque on purpose. Clients must not build one, so its shape can
// change without breaking them.
func encodeCursor(seen time.Time, id pgtype.UUID) string {
	raw := seen.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cur string) (pgtype.Timestamptz, pgtype.UUID, error) {
	var ts pgtype.Timestamptz
	var id pgtype.UUID
	if cur == "" {
		return ts, id, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return ts, id, fmt.Errorf("%w: malformed cursor", ErrInvalidInput)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return ts, id, fmt.Errorf("%w: malformed cursor", ErrInvalidInput)
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return ts, id, fmt.Errorf("%w: malformed cursor", ErrInvalidInput)
	}
	uid, err := parseUUID(parts[1])
	if err != nil {
		return ts, id, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, uid, nil
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return u, fmt.Errorf("%w: not a uuid", ErrInvalidInput)
	}
	return u, nil
}
