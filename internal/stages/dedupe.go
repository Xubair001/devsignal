package stages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/dedupe"
	"github.com/Xubair001/devsignal/internal/pipeline"
	"github.com/Xubair001/devsignal/internal/store"
)

// MaxBlockCandidates caps the comparison set. A pathological block (a company
// posting hundreds of near-identical roles) must not turn one item's processing
// into a long scan; the cap is logged so the truncation is never silent.
const MaxBlockCandidates = 50

// Deduper links a posting to an existing canonical row when they are the same
// real-world job. Runs on 'normalized', advances to 'deduped'.
//
// Merges are reversible by construction: the loser is retained with merged_into
// set, its source rows are moved, and the decision is recorded. A false merge
// hides a real job from the user permanently, so being able to undo one is not
// optional.
type Deduper struct {
	pool *pgxpool.Pool
	q    *store.Queries
	log  *slog.Logger
}

func NewDeduper(pool *pgxpool.Pool, log *slog.Logger) *Deduper {
	return &Deduper{pool: pool, q: store.New(pool), log: log}
}

func (d *Deduper) Handle(ctx context.Context, it pipeline.Item) error {
	me, err := d.q.GetOpportunityForDedupe(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if me.BlockKey == nil || *me.BlockKey == "" {
		// Nothing to compare against without a block key. Not an error: the row
		// simply proceeds unmerged.
		return nil
	}

	cands, err := d.q.FindBlockCandidates(ctx, store.FindBlockCandidatesParams{
		BlockKey:      me.BlockKey,
		ExcludeID:     it.ID,
		MaxCandidates: MaxBlockCandidates,
	})
	if err != nil {
		return fmt.Errorf("candidates: %w", err)
	}
	if len(cands) == MaxBlockCandidates {
		// Never truncate silently: a saturated block means dedup quality is
		// degraded for this row and someone should know.
		d.log.Warn("dedup block saturated; comparison truncated",
			"block_key", *me.BlockKey, "cap", MaxBlockCandidates)
	}

	mine := dedupe.Candidate{
		CompanyID:   me.CompanyID.String(),
		ATSType:     deref(me.AtsType),
		ATSJobID:    deref(me.AtsJobID),
		ApplyURL:    deref(me.ApplyUrl),
		ContentHash: me.ContentHash,
		SimHash:     unsignedHash(me.Simhash),
		GeoScope:    deref(me.RemoteGeoScope),
		TextLen:     int(me.TextLen),
		Title:       me.TitleNormalized,
		Country:     deref(me.LocationCountry),
	}

	for _, c := range cands {
		other := dedupe.Candidate{
			CompanyID:   c.CompanyID.String(),
			ATSType:     deref(c.AtsType),
			ATSJobID:    deref(c.AtsJobID),
			ApplyURL:    deref(c.ApplyUrl),
			ContentHash: c.ContentHash,
			SimHash:     unsignedHash(c.Simhash),
			GeoScope:    deref(c.RemoteGeoScope),
			TextLen:     int(c.TextLen),
			Title:       c.TitleNormalized,
			Country:     deref(c.LocationCountry),
		}
		v := dedupe.Decide(mine, other)
		if !v.Same {
			continue
		}
		if !v.AutoApply() {
			d.queue(ctx, it.ID, c.ID, v)
			continue
		}
		// Merge into the OLDER row: first_seen_at is ours and trustworthy, so the
		// earlier sighting is the canonical one and its age is preserved.
		if err := d.merge(ctx, it.ID, c.ID, *me.BlockKey, v); err != nil {
			return err
		}
		// This row is now merged away, and merge bumped its version. It must not
		// be advanced: it is no longer a canonical posting.
		return pipeline.ErrHandled
	}
	return nil
}

func (d *Deduper) merge(ctx context.Context, from, into pgtype.UUID, blockKey string, v dedupe.Verdict) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)

	// Serialize within the block, then re-read both sides. Two workers in one
	// block could otherwise each merge into the other and create an A->B->A
	// cycle, because both made their decision from a pre-lock read.
	if blockKey != "" {
		if err := q.LockBlock(ctx, blockKey); err != nil {
			return fmt.Errorf("lock block: %w", err)
		}
	}
	for _, id := range []pgtype.UUID{from, into} {
		ok, err := q.IsUnmerged(ctx, id)
		if err != nil {
			return fmt.Errorf("recheck %s: %w", id.String(), err)
		}
		if !ok {
			// Someone merged one side while we were deciding. Yield.
			return pipeline.ErrVersionConflict
		}
	}

	reason := string(v.Reason)
	conf := v.Confidence

	movedIDs, err := q.MoveSourceRows(ctx, store.MoveSourceRowsParams{
		IntoID: into, Reason: &reason, Confidence: &conf, FromID: from,
	})
	if err != nil {
		return fmt.Errorf("move source rows: %w", err)
	}

	marked, err := q.MarkMerged(ctx, store.MarkMergedParams{IntoID: into, FromID: from})
	if err != nil {
		return fmt.Errorf("mark merged: %w", err)
	}
	if marked == 0 {
		// Already merged by a concurrent worker. Yield rather than double-record.
		return pipeline.ErrVersionConflict
	}

	if _, err := q.RecordMerge(ctx, store.RecordMergeParams{
		FromOpportunityID: from,
		IntoOpportunityID: into,
		Reason:            reason,
		Confidence:        &conf,
		SourceRowsMoved:   int32(len(movedIDs)),
		MergedBy:          "dedupe",
		// The ids, not just the count. Without them the merge cannot be reversed,
		// and hard rule 11 requires that it can be.
		MovedSourceIds: movedIDs,
	}); err != nil {
		return fmt.Errorf("record merge: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	d.log.Info("merged duplicate",
		"from", from.String(), "into", into.String(),
		"reason", reason, "confidence", conf, "source_rows_moved", len(movedIDs))
	return nil
}

// queue records a pair that looked the same but not confidently enough to act
// on. Nothing is lost and nothing is hidden: a human decides.
func (d *Deduper) queue(ctx context.Context, left, right pgtype.UUID, v dedupe.Verdict) {
	why := fmt.Sprintf("confidence %.2f below auto-merge floor %.2f",
		v.Confidence, dedupe.MinAutoMergeConfidence)
	if err := d.q.QueueMergeCandidate(ctx, store.QueueMergeCandidateParams{
		LeftOpportunityID:  left,
		RightOpportunityID: right,
		Reason:             string(v.Reason),
		Confidence:         v.Confidence,
		WithheldBecause:    why,
	}); err != nil {
		d.log.Error("queueing merge candidate", "err", err)
		return
	}
	d.log.Info("merge withheld for review",
		"left", left.String(), "right", right.String(),
		"reason", v.Reason, "confidence", v.Confidence)
}

// unsignedHash reverses the int64 storage encoding.
func unsignedHash(p *int64) uint64 {
	if p == nil {
		return 0
	}
	return uint64(*p)
}

// SweepBlocks re-examines every block with more than one member.
//
// This exists because per-item dedup is order-dependent: on a bulk first ingest
// two identical postings can each finish before the other's block_key is
// visible, so neither ever sees the other and both survive as duplicates. The
// sweep makes dedup eventually consistent instead of dependent on arrival order.
//
// Observed on real data: two identically-worded postings from one board came
// through with the same SimHash and were never compared.
func (d *Deduper) SweepBlocks(ctx context.Context, batch int32) (int, error) {
	blocks, err := d.q.FindMultiMemberBlocks(ctx, batch)
	if err != nil {
		return 0, fmt.Errorf("find blocks: %w", err)
	}

	merged := 0
	for _, b := range blocks {
		if b.BlockKey == nil {
			continue
		}
		n, err := d.sweepOne(ctx, *b.BlockKey)
		if err != nil {
			d.log.Error("sweeping block", "block_key", *b.BlockKey, "err", err)
			continue
		}
		merged += n
	}
	if merged > 0 {
		d.log.Info("dedup sweep merged duplicates", "count", merged, "blocks", len(blocks))
	}
	return merged, nil
}

func (d *Deduper) sweepOne(ctx context.Context, blockKey string) (int, error) {
	members, err := d.q.ListBlockMembers(ctx, &blockKey)
	if err != nil {
		return 0, fmt.Errorf("list members: %w", err)
	}
	if len(members) < 2 {
		return 0, nil
	}

	// Members arrive oldest-first, so the survivor of any pair is the earlier
	// sighting: first_seen_at is ours and trustworthy, and preserving it keeps
	// the age signal honest.
	gone := make(map[string]bool, len(members))
	merges := 0

	for i := 0; i < len(members); i++ {
		if gone[members[i].ID.String()] {
			continue
		}
		keeper := toCandidate(members[i])
		for j := i + 1; j < len(members); j++ {
			if gone[members[j].ID.String()] {
				continue
			}
			// Defence in depth. The query no longer duplicates a posting, but a
			// self-merge is caught by a CHECK constraint mid-transaction, and
			// discovering it there means an error log and a rolled-back sweep
			// rather than a skipped comparison. Cheap to assert, expensive to
			// rediscover.
			if members[j].ID == members[i].ID {
				continue
			}
			other := toCandidate(members[j])
			v := dedupe.Decide(keeper, other)
			if !v.Same {
				continue
			}
			if !v.AutoApply() {
				d.queue(ctx, members[j].ID, members[i].ID, v)
				continue
			}
			if err := d.merge(ctx, members[j].ID, members[i].ID, blockKey, v); err != nil {
				if errors.Is(err, pipeline.ErrVersionConflict) {
					gone[members[j].ID.String()] = true
					continue
				}
				return merges, err
			}
			gone[members[j].ID.String()] = true
			merges++
		}
	}
	return merges, nil
}

func toCandidate(m store.ListBlockMembersRow) dedupe.Candidate {
	return dedupe.Candidate{
		CompanyID:   m.CompanyID.String(),
		ATSType:     deref(m.AtsType),
		ATSJobID:    deref(m.AtsJobID),
		ApplyURL:    deref(m.ApplyUrl),
		ContentHash: m.ContentHash,
		SimHash:     unsignedHash(m.Simhash),
		GeoScope:    deref(m.RemoteGeoScope),
		TextLen:     int(m.TextLen),
		Title:       m.TitleNormalized,
		Country:     deref(m.LocationCountry),
	}
}

// DedupeSweeper runs SweepBlocks on an interval.
type DedupeSweeper struct {
	d        *Deduper
	Interval time.Duration
	Batch    int32
}

func NewDedupeSweeper(d *Deduper) *DedupeSweeper {
	return &DedupeSweeper{d: d, Interval: 2 * time.Minute, Batch: 200}
}

func (s *DedupeSweeper) Run(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	s.d.log.Info("dedup sweeper started", "interval", s.Interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := s.d.SweepBlocks(ctx, s.Batch); err != nil && ctx.Err() == nil {
				s.d.log.Error("dedup sweep", "err", err)
			}
		}
	}
}
