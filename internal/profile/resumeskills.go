package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/enrich"
	"github.com/Xubair001/devsignal/internal/skill"
	"github.com/Xubair001/devsignal/internal/store"
)

// SkillExtractor reads skills out of a resume.
//
// Its own type rather than a method on Service, because it is the ONLY thing in
// this package that talks to an external model, and keeping that boundary
// visible is most of the point. Privacy rule 2 lives here: the text is redacted
// before it leaves, and what left is recorded.
type SkillExtractor struct {
	svc      *Service
	provider enrich.Provider
	ontology *skill.Ontology
	log      Logger
}

// Logger is the subset of slog this needs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewSkillExtractor builds one. A nil provider is an error rather than a
// degraded mode: unlike a posting, there is no useful partial result here, and a
// caller that cannot extract should not be scheduling extractions.
func NewSkillExtractor(
	svc *Service, p enrich.Provider, log Logger,
) (*SkillExtractor, error) {
	if p == nil {
		return nil, errors.New("profile: a provider is required to extract resume skills")
	}
	o, err := skill.Load()
	if err != nil {
		return nil, fmt.Errorf("profile: loading ontology: %w", err)
	}
	return &SkillExtractor{svc: svc, provider: p, ontology: o, log: log}, nil
}

// Result is what one resume produced.
type Result struct {
	ResumeID string
	// Found is how many skills the model returned; Resolved is how many the
	// ontology could place. The GAP is the review signal: twenty found and two
	// resolved means the vocabulary is missing something, not that the person
	// has two skills.
	Found      int
	Resolved   int
	Unresolved []string
	Redaction  Redaction
	// YearsClaimed and SeniorityClaimed are what the resume EVIDENCED. They are
	// recorded, never written onto the profile — see write().
	YearsClaimed     *int
	SeniorityClaimed string
}

// ExtractPending processes resumes whose skills are missing or stale.
func (e *SkillExtractor) ExtractPending(ctx context.Context, limit int32) ([]Result, error) {
	rows, err := e.svc.q.ResumesNeedingSkills(ctx, store.ResumesNeedingSkillsParams{
		PromptVersion:    enrich.ResumePromptVersion,
		ModelID:          e.provider.ModelID(),
		RedactionVersion: RedactionVersion,
		MaxRows:          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("profile: finding resumes: %w", err)
	}

	out := make([]Result, 0, len(rows))
	for _, row := range rows {
		res, err := e.ExtractFor(ctx, row.UserID, row.ID, deref(row.TextObjectKey))
		if err != nil {
			// One resume's failure must not end the batch. Logged with the user id
			// and nothing about the person (hard rule 13).
			e.log.Error("extracting resume skills",
				"user_id", row.UserID.String(), "resume_id", row.ID.String(), "err", err)
			continue
		}
		out = append(out, res)
	}
	return out, nil
}

// ExtractFor reads one resume's skills and writes them to the profile.
//
// The order matters. Redact, then send, then record what was sent, then resolve,
// then write — so a failure at any point leaves either nothing or a complete,
// dated record, never skills whose provenance is unknown.
func (e *SkillExtractor) ExtractFor(
	ctx context.Context, userID, resumeID pgtype.UUID, textKey string,
) (Result, error) {
	res := Result{ResumeID: resumeID.String()}
	if textKey == "" {
		return res, errors.New("resume has no extracted text")
	}

	raw, err := e.svc.blob.Get(ctx, textKey)
	if err != nil {
		return res, fmt.Errorf("reading resume text: %w", err)
	}

	// PRIVACY RULE 2. Nothing above this line has left the process; nothing below
	// it sees the original document.
	redacted, rec := Redact(string(raw))
	res.Redaction = rec
	if len(strings.TrimSpace(redacted)) < enrich.MinTextToExtract {
		// Not an error. A scanned PDF with no text layer, or a one-page document
		// that is mostly contact details, legitimately yields nothing — and paying
		// for a model call on it would return an empty result we would then record
		// as authoritative.
		e.log.Info("resume too short to extract after redaction",
			"user_id", userID.String(), "chars", rec.OutChars)
		return res, nil
	}

	out, err := e.provider.ExtractWith(ctx, enrich.ResumeSkillsTask(), redacted)
	if err != nil {
		return res, fmt.Errorf("model call: %w", err)
	}

	var parsed enrich.ResumeSkills
	if uerr := json.Unmarshal(out.JSON, &parsed); uerr != nil {
		return res, fmt.Errorf("%w: %w", enrich.ErrInvalidOutput, uerr)
	}
	res.Found = len(parsed.Skills)
	res.YearsClaimed = parsed.YearsExperienceMin
	if parsed.Seniority != "" && parsed.Seniority != enrich.UnknownValue {
		res.SeniorityClaimed = parsed.Seniority
	}

	// Resolve through the SAME ontology the postings go through. That is the
	// whole reason the skill factors can score at all: a resume saying "Golang"
	// and a posting saying "Go" have to reach one row.
	//
	// No create path, exactly like the manual editor. A model's paraphrase of a
	// technology is not evidence a vocabulary entry should exist — an
	// unrecognised phrase here would become a skill that then matches no posting.
	resolved := make(map[string]bool)
	for _, sk := range parsed.Skills {
		slug, ok := e.ontology.Resolve(sk.Name)
		if !ok {
			res.Unresolved = append(res.Unresolved, sk.Name)
			continue
		}
		resolved[slug] = true
	}
	res.Resolved = len(resolved)

	if err := e.write(ctx, userID, resumeID, res, resolved, out.Model); err != nil {
		return res, err
	}
	return res, nil
}

func (e *SkillExtractor) write(
	ctx context.Context, userID, resumeID pgtype.UUID, res Result,
	resolved map[string]bool, modelID string,
) error {
	tx, err := e.svc.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := e.svc.q.WithTx(tx)

	// Replace only what a RESUME contributed. A manual entry is a stated claim
	// and a resume reading is evidence; refreshing the evidence is not a reason
	// to discard the claim.
	if _, derr := q.DeleteResumeOriginSkills(ctx, userID); derr != nil {
		return fmt.Errorf("clearing resume skills: %w", derr)
	}

	for slug := range resolved {
		row, rerr := q.ProfileSkillByAlias(ctx, slug)
		if rerr != nil {
			if errors.Is(rerr, pgx.ErrNoRows) {
				// The ontology resolved it but the database has not been seeded with
				// that slug. A seeding gap, not a data error — skip rather than fail
				// the whole extraction.
				continue
			}
			return fmt.Errorf("looking up skill %q: %w", slug, rerr)
		}
		if perr := q.UpsertProfileSkill(ctx, store.UpsertProfileSkillParams{
			UserID: userID, SkillID: row.ID, Origin: "resume",
		}); perr != nil {
			return fmt.Errorf("saving skill %q: %w", slug, perr)
		}
	}

	// The record of what left our boundary. Counts, never values.
	if rerr := q.RecordResumeSkillExtraction(ctx,
		store.RecordResumeSkillExtractionParams{
			ID:               resumeID,
			ModelID:          ptr(modelID),
			PromptVersion:    ptr(enrich.ResumePromptVersion),
			SchemaVersion:    ptr(enrich.ResumeSchemaVersion),
			RedactionVersion: ptr(RedactionVersion),
			FieldSet:         ptr(res.Redaction.FieldSet()),
			RedactedChars:    ptr(int32(res.Redaction.InChars - res.Redaction.OutChars)),
			SentChars:        ptr(int32(res.Redaction.OutChars)),
			SkillsFound:      ptr(int32(res.Found)),
			SkillsResolved:   ptr(int32(res.Resolved)),
			YearsClaimed:     smallPtr(res.YearsClaimed),
			SeniorityClaimed: nilIfEmpty(res.SeniorityClaimed),
		}); rerr != nil {
		return fmt.Errorf("recording extraction: %w", rerr)
	}

	// The profile version bump is what invalidates cached fit scores. A skill
	// change moves a score, so it is not optional — and it happens in the same
	// transaction as the change so the two cannot disagree.
	//
	// NOT written: years_experience and seniority_ordinal on the profile. Those
	// are the user's OWN stated preferences, and overwriting what a person typed
	// with what a model read off their resume is the same category error as
	// presenting an imputed salary as the employer's. The claim is recorded on
	// the resume row and surfaced for them to accept.
	if terr := q.TouchProfileVersion(ctx, userID); terr != nil {
		return fmt.Errorf("bumping profile version: %w", terr)
	}
	return tx.Commit(ctx)
}

func ptr[T any](v T) *T { return &v }

func smallPtr(v *int) *int16 {
	if v == nil {
		return nil
	}
	s := int16(*v)
	return &s
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
