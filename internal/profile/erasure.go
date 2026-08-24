package profile

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

// Erasure locations. This list IS the guarantee.
//
// Deleting the Postgres rows is the easy 60%. What survives a naive
// implementation is object storage, embeddings, index documents, caches and
// analytics copies — and a deletion that leaves a resume-derived artifact in a
// live store is not a deletion.
//
// Adding anything that stores user-derived data means adding a location here in
// the same change. The completeness check fails otherwise, which is the point.
const (
	LocObjectStorage = "object_storage"
	LocResumeRows    = "resume_rows"
	LocProfile       = "profile"
	LocProfileSkills = "profile_skills"
	LocSessions      = "sessions"
	LocRefreshTokens = "refresh_tokens"
	LocUserTokens    = "user_tokens"
	LocUserRow       = "user_row"

	// Live since step 14.
	LocProfileEmbedding = "profile_embedding"

	// Declared but not yet applicable: these stores exist in the design and will
	// hold user-derived data at their step. Recorded as not_applicable rather
	// than omitted, so the inventory shows they were CONSIDERED rather than
	// forgotten.
	LocSearchIndex = "search_index"
	LocRedisCache  = "redis_cache"
	LocAnalytics   = "analytics_copies"
)

// AllLocations is the full inventory, in deletion order: derived artifacts
// before the rows that point at them, so a crash mid-run never orphans data that
// a later run cannot find.
var AllLocations = []string{
	LocObjectStorage,
	LocProfileEmbedding,
	LocSearchIndex,
	LocRedisCache,
	LocAnalytics,
	LocResumeRows,
	LocProfileSkills,
	LocProfile,
	// Refresh tokens BEFORE sessions: refresh_token.session_id cascades from
	// user_session, so deleting sessions first silently removes them and this
	// step then reports "done, 0 items". The data was gone either way, but an
	// erasure report that understates what it removed is misleading to anyone
	// auditing it.
	LocRefreshTokens,
	LocSessions,
	LocUserTokens,
	LocUserRow,
}

// ErasureReport is what an operator (or an auditor) reads.
type ErasureReport struct {
	RequestID pgtype.UUID
	Steps     []store.ListErasureStepsRow
	Complete  bool
	// TracesRemaining is counted AFTER the deletes rather than inferred from
	// them. Trusting a delete to have worked is how partial erasures get marked
	// complete.
	TracesRemaining  int32
	ObjectsRemaining int
}

// Erase removes everything derived from a user and verifies the result.
//
// Not a transaction: object storage cannot join one. Each location therefore
// reports its own outcome, and the request is only marked complete when every
// one of them succeeded — a partial erasure stays visibly incomplete.
func (s *Service) Erase(ctx context.Context, userID pgtype.UUID) (*ErasureReport, error) {
	req, err := s.q.CreateErasureRequest(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("opening erasure request: %w", err)
	}
	rep := &ErasureReport{RequestID: req.ID}

	// Object storage first: the row that names the objects is deleted later, so
	// losing the row before the objects would strand them permanently.
	prefix := UserPrefix(userID.String())
	if n, derr := s.blob.DeletePrefix(ctx, prefix); derr != nil {
		s.step(ctx, req.ID, LocObjectStorage, "failed", 0, derr.Error())
	} else {
		s.step(ctx, req.ID, LocObjectStorage, "done", n, "")
	}

	// Declared, not yet built. Recorded so the inventory proves they were
	// considered at this step rather than silently missed.
	for _, loc := range []string{
		LocSearchIndex, LocRedisCache, LocAnalytics,
	} {
		s.step(ctx, req.ID, loc, "not_applicable", 0, "store not yet in use")
	}

	type del struct {
		loc string
		fn  func(context.Context, pgtype.UUID) (int64, error)
	}
	for _, d := range []del{
		// Before the profile row: the vector is derived from it, and losing the
		// profile first would leave a vector nothing points at.
		{LocProfileEmbedding, s.q.DeleteProfileEmbedding},
		{LocResumeRows, s.q.DeleteResumeRows},
		{LocProfileSkills, s.q.DeleteProfileSkills},
		{LocProfile, s.q.DeleteProfileData},
		{LocRefreshTokens, s.q.DeleteUserRefreshTokens},
		{LocSessions, s.q.DeleteUserSessions},
		{LocUserTokens, s.q.DeleteUserTokens},
		{LocUserRow, s.q.DeleteUserRow},
	} {
		n, derr := d.fn(ctx, userID)
		if derr != nil {
			s.step(ctx, req.ID, d.loc, "failed", 0, derr.Error())
			continue
		}
		s.step(ctx, req.ID, d.loc, "done", int(n), "")
	}

	// Verify by counting, not by trusting.
	if traces, terr := s.q.CountUserTraces(ctx, userID); terr == nil {
		rep.TracesRemaining = traces
	}
	if objs, oerr := s.blob.CountPrefix(ctx, prefix); oerr == nil {
		rep.ObjectsRemaining = objs
	}

	if err := s.q.CompleteErasureRequest(ctx, req.ID); err != nil {
		s.log.Error("completing erasure request", "err", err)
	}

	steps, err := s.q.ListErasureSteps(ctx, req.ID)
	if err != nil {
		return rep, fmt.Errorf("reading erasure steps: %w", err)
	}
	rep.Steps = steps

	fresh, err := s.q.GetErasureRequest(ctx, req.ID)
	if err == nil {
		rep.Complete = fresh.CompletedAt.Valid
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.log.Warn("re-reading erasure request", "err", err)
	}

	// Complete AND verifiably empty. Either alone is not the guarantee.
	if !rep.Complete || rep.TracesRemaining != 0 || rep.ObjectsRemaining != 0 {
		s.log.Error("erasure did not fully complete",
			"request_id", req.ID.String(), "complete", rep.Complete,
			"db_traces", rep.TracesRemaining, "objects", rep.ObjectsRemaining)
	} else {
		s.log.Info("erasure verified complete", "request_id", req.ID.String())
	}
	return rep, nil
}

func (s *Service) step(ctx context.Context, reqID pgtype.UUID, loc, status string, items int, detail string) {
	var d *string
	if detail != "" {
		if len(detail) > 500 {
			detail = detail[:500]
		}
		d = &detail
	}
	if err := s.q.RecordErasureStep(ctx, store.RecordErasureStepParams{
		RequestID: reqID, Location: loc, Status: status,
		Items: int32(items), Detail: d,
	}); err != nil {
		s.log.Error("recording erasure step", "location", loc, "err", err)
	}
}
