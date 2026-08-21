// Package ingest turns parsed postings into canonical rows with provenance.
//
// Three invariants, all from the blueprint's hard rules:
//   - provenance is separable, so merges stay reversible
//   - our own observations outrank the source's claims
//   - closure is inferred from a SUCCESSFUL poll that did not see the posting
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/source"
	"github.com/Xubair001/devsignal/internal/store"
)

// MaxConsecutiveMisses is how many successful polls must miss a posting before
// it is considered closed. More than one, because a single board hiccup must not
// close a live job.
const MaxConsecutiveMisses = 2

type Result struct {
	Fetched     int
	Created     int
	Updated     int
	Unchanged   int
	Skipped     int
	Missed      int64
	Closed      int64
	NotModified bool
}

// ParseYield is the fraction of fetched documents that produced a usable record.
// Monitoring this rather than error rate is what catches parser rot: the failure
// is a parser that still returns a row, with fields quietly empty.
func (r Result) ParseYield() float64 {
	total := r.Created + r.Updated + r.Unchanged + r.Skipped
	if total == 0 {
		return 1
	}
	return float64(total-r.Skipped) / float64(total)
}

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), log: log}
}

// RunSource fetches one source and reconciles the result.
//
// Returns the cursor to persist. A not-modified response is a SUCCESS: it proves
// the board is reachable and unchanged, so liveness is untouched and nothing is
// closed.
func (s *Service) RunSource(ctx context.Context, sourceID pgtype.UUID, a source.Adapter, cur source.Cursor) (Result, source.Cursor, error) {
	var res Result

	docs, next, err := a.Fetch(ctx, cur)
	if err != nil {
		if errors.Is(err, source.ErrNotModified) {
			res.NotModified = true
			// Deliberately no liveness bookkeeping: nothing changed, and we did
			// not observe the individual postings, so we must not count misses.
			return res, cur, nil
		}
		return res, cur, fmt.Errorf("fetch: %w", err)
	}

	seen := make([]string, 0, 256)
	for _, doc := range docs {
		postings, perr := a.Parse(doc)
		if perr != nil {
			// A parse failure on a whole document is a source-health event, not a
			// per-record skip: quarantine-worthy if it persists.
			return res, cur, fmt.Errorf("parse: %w", perr)
		}
		res.Fetched += len(postings)

		for _, p := range postings {
			if p.ATSJobID == "" || p.Title == "" {
				res.Skipped++
				continue
			}
			outcome, uerr := s.upsert(ctx, sourceID, a, p, next.ETag)
			if uerr != nil {
				res.Skipped++
				s.log.Error("upserting posting", "ats_job_id", p.ATSJobID, "err", uerr)
				continue
			}
			switch outcome {
			case outcomeCreated:
				res.Created++
			case outcomeUpdated:
				res.Updated++
			default:
				res.Unchanged++
			}
			seen = append(seen, p.SourceJobID)
		}
	}

	// Absence-based liveness, only now that the poll has succeeded.
	missed, err := s.q.BumpMissesForAbsent(ctx, store.BumpMissesForAbsentParams{
		SourceID: sourceID, SeenIds: seen,
	})
	if err != nil {
		return res, next, fmt.Errorf("liveness misses: %w", err)
	}
	res.Missed = missed

	closed, err := s.q.CloseMissedOpportunities(ctx, MaxConsecutiveMisses)
	if err != nil {
		return res, next, fmt.Errorf("closing missed: %w", err)
	}
	res.Closed = closed

	return res, next, nil
}

type outcome int

const (
	outcomeUnchanged outcome = iota
	outcomeCreated
	outcomeUpdated
)

func (s *Service) upsert(ctx context.Context, sourceID pgtype.UUID, a source.Adapter, p source.ParsedPosting, etag string) (outcome, error) {
	var out outcome

	err := s.inTx(ctx, func(q *store.Queries) error {
		existing, err := q.FindSourceRow(ctx, store.FindSourceRowParams{
			SourceID: sourceID, SourceJobID: p.SourceJobID,
		})
		if err == nil {
			// Seen before through this source.
			if string(existing.ContentHash) == string(p.ContentHash) {
				if _, err := q.MarkOpportunitySeen(ctx, existing.OpportunityID); err != nil {
					return err
				}
				out = outcomeUnchanged
			} else {
				if err := s.applyUpdate(ctx, q, existing.OpportunityID, p); err != nil {
					return err
				}
				out = outcomeUpdated
			}
			return q.TouchSourceRow(ctx, store.TouchSourceRowParams{
				ID: existing.ID, ContentHash: p.ContentHash,
				Etag: nz(etag), RawObjectKey: nil,
			})
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find source row: %w", err)
		}

		// New to this source. Does another source already have this exact ATS
		// posting? If so, link to the existing canonical row rather than
		// creating a duplicate.
		company, err := s.resolveCompany(ctx, q, a, p)
		if err != nil {
			return err
		}

		var oppID pgtype.UUID
		mergeReason := "exact_ats"
		linked, err := q.FindCanonicalByATS(ctx, store.FindCanonicalByATSParams{
			AtsType: nz(p.ATSType), AtsJobID: nz(p.ATSJobID),
		})
		switch {
		case err == nil:
			oppID = linked
			out = outcomeUnchanged
		case errors.Is(err, pgx.ErrNoRows):
			created, cerr := q.InsertOpportunityFromPosting(ctx, store.InsertOpportunityFromPostingParams{
				CompanyID:              company.ID,
				TitleRaw:               p.Title,
				TitleNormalized:        normalizeTitle(p.Title),
				DescriptionText:        nz(p.DescriptionHTML),
				WorkMode:               nz(p.WorkMode),
				LocationRegion:         nz(p.LocationRaw),
				Language:               nz(p.Language),
				ApplyMethod:            nz(p.ATSType),
				AtsType:                nz(p.ATSType),
				SourceReportedPostedAt: optTime(p.SourceReportedPostedAt),
				ContentHash:            p.ContentHash,
			})
			if cerr != nil {
				return fmt.Errorf("insert opportunity: %w", cerr)
			}
			oppID = created.ID
			out = outcomeCreated
			mergeReason = "" // first sighting: nothing was merged
		default:
			return fmt.Errorf("find canonical: %w", err)
		}

		var reason *string
		var conf *float32
		if mergeReason != "" {
			reason = &mergeReason
			c := float32(1)
			conf = &c
		}
		_, err = q.InsertSourceRow(ctx, store.InsertSourceRowParams{
			OpportunityID: oppID, SourceID: sourceID, SourceJobID: p.SourceJobID,
			AtsType: nz(p.ATSType), AtsJobID: nz(p.ATSJobID),
			ApplyUrl: nz(p.ApplyURL), RawObjectKey: nil,
			ContentHash: p.ContentHash, Etag: nz(etag),
			MergeReason: reason, MergeConfidence: conf, MergedBy: nz("ingest"),
		})
		return err
	})
	return out, err
}

func (s *Service) applyUpdate(ctx context.Context, q *store.Queries, oppID pgtype.UUID, p source.ParsedPosting) error {
	n, err := q.UpdateOpportunityFromPosting(ctx, store.UpdateOpportunityFromPostingParams{
		ID: oppID, TitleRaw: p.Title, TitleNormalized: normalizeTitle(p.Title),
		DescriptionText: nz(p.DescriptionHTML), WorkMode: nz(p.WorkMode),
		LocationRegion: nz(p.LocationRaw), Language: nz(p.Language),
		SourceReportedPostedAt: optTime(p.SourceReportedPostedAt),
		ContentHash:            p.ContentHash,
	})
	if err != nil {
		return fmt.Errorf("update opportunity: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update opportunity %s: no rows", oppID.String())
	}
	return nil
}

// resolveCompany implements the deterministic part of resolution order: the ATS
// board token, then a domain the source revealed. Alias and fuzzy matching are
// step 8 and are never auto-merged.
func (s *Service) resolveCompany(ctx context.Context, q *store.Queries, a source.Adapter, p source.ParsedPosting) (store.Company, error) {
	domain := strings.ToLower(strings.TrimSpace(p.CompanyDomain))
	if domain == "" {
		// No domain revealed. The board token IS a deterministic identity for a
		// Tier-A source, so derive a stable placeholder rather than matching on
		// the company name, which is not an identity.
		domain = boardIdentity(a.ID())
	}
	name := p.CompanyName
	if name == "" {
		name = domain
	}
	c, err := q.UpsertCompanyByDomain(ctx, store.UpsertCompanyByDomainParams{
		CanonicalDomain: domain, DisplayName: name,
	})
	if err != nil {
		return store.Company{}, fmt.Errorf("resolve company: %w", err)
	}
	return c, nil
}

// boardIdentity turns "greenhouse:gitlab" into a stable pseudo-domain. It is
// deliberately obvious that this is not a real domain, so it can be reconciled
// against a real one later without ambiguity.
func boardIdentity(adapterID string) string {
	parts := strings.SplitN(adapterID, ":", 2)
	if len(parts) == 2 {
		return parts[1] + "." + parts[0] + ".ats.invalid"
	}
	return adapterID + ".ats.invalid"
}

func normalizeTitle(t string) string {
	return strings.ToLower(strings.Join(strings.Fields(t), " "))
}

func (s *Service) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
