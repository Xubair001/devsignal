package profile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Xubair001/devsignal/internal/store"
	"github.com/Xubair001/devsignal/pkg/blob"
)

var (
	ErrNotFound     = errors.New("profile not found")
	ErrInvalidInput = errors.New("invalid input")
)

type Service struct {
	pool *pgxpool.Pool
	q    *store.Queries
	blob *blob.Store
	log  *slog.Logger
}

func NewService(pool *pgxpool.Pool, b *blob.Store, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), blob: b, log: log}
}

// ---------------------------------------------------------------- profile

type Input struct {
	Headline           *string
	YearsExperience    *int16
	SeniorityOrdinal   *int16
	IsManagement       bool
	TargetRoleFamilies []string
	TargetCountries    []string
	WorkModePreference *string
	Languages          []string
	MinSalaryMinor     *int64
	SalaryCurrency     *string
	SalaryPeriod       *string
	WorkAuthorization  []byte
}

func (s *Service) Save(ctx context.Context, userID, tenantID pgtype.UUID, in Input) (store.Profile, error) {
	// A column DEFAULT only applies when the column is OMITTED, not when NULL is
	// passed explicitly. These columns are NOT NULL with an empty-array default,
	// so a nil slice from a caller who simply left the field out would violate
	// the constraint. Normalizing here keeps "unset" meaning "empty".
	auth := in.WorkAuthorization
	if len(auth) == 0 {
		auth = []byte("{}")
	}
	families := in.TargetRoleFamilies
	if families == nil {
		families = []string{}
	}
	countries := in.TargetCountries
	if countries == nil {
		countries = []string{}
	}
	langs := in.Languages
	if langs == nil {
		langs = []string{}
	}
	p, err := s.q.UpsertProfile(ctx, store.UpsertProfileParams{
		UserID: userID, TenantID: tenantID,
		Headline: in.Headline, YearsExperience: in.YearsExperience,
		SeniorityOrdinal: in.SeniorityOrdinal, IsManagement: in.IsManagement,
		TargetRoleFamilies: families, TargetCountries: countries,
		WorkModePreference: in.WorkModePreference, Languages: langs,
		MinSalaryMinor: in.MinSalaryMinor, SalaryCurrency: in.SalaryCurrency,
		SalaryPeriod: in.SalaryPeriod, WorkAuthorization: auth,
	})
	if err != nil {
		return store.Profile{}, fmt.Errorf("saving profile: %w", err)
	}
	// user_id only. A profile is PII and none of its content belongs in a log.
	s.log.Info("profile saved", "user_id", userID.String(), "profile_version", p.ProfileVersion)
	return p, nil
}

func (s *Service) Get(ctx context.Context, userID pgtype.UUID) (store.Profile, []store.ListProfileSkillsRow, error) {
	p, err := s.q.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Profile{}, nil, ErrNotFound
		}
		return store.Profile{}, nil, fmt.Errorf("loading profile: %w", err)
	}
	skills, err := s.q.ListProfileSkills(ctx, userID)
	if err != nil {
		return p, nil, fmt.Errorf("loading skills: %w", err)
	}
	return p, skills, nil
}

// ---------------------------------------------------------------- resume

type Upload struct {
	Filename    string
	ContentType string
	Body        []byte
}

// UploadResume stores the file, extracts its text, and stores that too.
//
// Both objects go to object storage under the user's prefix. The extracted text
// is the densest PII in the system, so keeping it out of Postgres keeps it out of
// database backups and query logs, and leaves exactly one place to delete.
func (s *Service) UploadResume(ctx context.Context, userID pgtype.UUID, up Upload) (store.Resume, error) {
	if len(up.Body) == 0 {
		return store.Resume{}, fmt.Errorf("%w: empty upload", ErrInvalidInput)
	}
	if len(up.Body) > MaxResumeBytes {
		return store.Resume{}, ErrTooLarge
	}
	if !SupportedType(up.ContentType) {
		return store.Resume{}, fmt.Errorf("%w: %q", ErrUnsupportedType, up.ContentType)
	}

	resumeID := uuid.NewString()
	ext := extensionFor(up.ContentType)
	key := ObjectKey(userID.String(), resumeID, ext)

	// Store the original first. If extraction fails we still hold the source
	// document, so a parser fix can re-run without asking the user to re-upload.
	if err := s.blob.Put(ctx, key, up.Body, normalizeContentType(up.ContentType)); err != nil {
		return store.Resume{}, fmt.Errorf("storing resume: %w", err)
	}

	rec, err := s.q.CreateResume(ctx, store.CreateResumeParams{
		UserID: userID, ObjectKey: key,
		Filename: &up.Filename, ContentType: &up.ContentType,
		SizeBytes: int64(len(up.Body)), Sha256: Fingerprint(up.Body),
	})
	if err != nil {
		// Orphaned object: remove it rather than leave PII with no row pointing
		// at it, which erasure-by-row would then miss.
		if derr := s.blob.Delete(ctx, key); derr != nil {
			s.log.Error("orphaned resume object left behind", "key", key, "err", derr)
		}
		return store.Resume{}, fmt.Errorf("recording resume: %w", err)
	}

	text, err := ExtractText(up.Body, up.ContentType)
	if err != nil {
		// Never store the error verbatim: it can echo document content.
		reason := classifyExtractError(err)
		if ferr := s.q.FailResume(ctx, store.FailResumeParams{
			ID: rec.ID, ParseError: &reason,
		}); ferr != nil {
			s.log.Error("recording extraction failure", "resume_id", rec.ID.String(), "err", ferr)
		}
		s.log.Warn("resume text extraction failed",
			"user_id", userID.String(), "resume_id", rec.ID.String(), "reason", reason)
		rec.ParseState = "failed"
		rec.ParseError = &reason
		return rec, nil // the upload itself succeeded
	}

	textKey := TextObjectKey(userID.String(), resumeID)
	if err := s.blob.Put(ctx, textKey, []byte(text), "text/plain"); err != nil {
		return rec, fmt.Errorf("storing extracted text: %w", err)
	}
	chars := int32(len(text))
	if err := s.q.SetResumeText(ctx, store.SetResumeTextParams{
		ID: rec.ID, TextObjectKey: &textKey, TextChars: &chars,
	}); err != nil {
		return rec, fmt.Errorf("recording extracted text: %w", err)
	}

	// Character count, never content.
	s.log.Info("resume ingested", "user_id", userID.String(),
		"resume_id", rec.ID.String(), "chars", chars)

	rec.TextObjectKey = &textKey
	rec.TextChars = &chars
	rec.ParseState = "text_extracted"
	return rec, nil
}

func (s *Service) ListResumes(ctx context.Context, userID pgtype.UUID) ([]store.Resume, error) {
	return s.q.ListUserResumes(ctx, userID)
}

// DeleteResume soft-deletes the row. The objects are removed by the erasure job,
// so a deletion is always tracked work with a verifiable outcome rather than a
// fire-and-forget call.
func (s *Service) DeleteResume(ctx context.Context, userID, resumeID pgtype.UUID) error {
	return s.q.SoftDeleteResume(ctx, store.SoftDeleteResumeParams{
		ID: resumeID, UserID: userID,
	})
}

func extensionFor(ct string) string {
	switch normalizeContentType(ct) {
	case TypePDF:
		return ".pdf"
	case TypeMD:
		return ".md"
	default:
		return ".txt"
	}
}

// classifyExtractError maps to a fixed vocabulary. The underlying error can
// contain document bytes, and this string is persisted and logged.
func classifyExtractError(err error) string {
	switch {
	case errors.Is(err, ErrNoTextFound):
		return "no_text_found: the document appears to be images with no text layer"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrUnsupportedType):
		return "unsupported_or_unparseable"
	default:
		return "extraction_failed"
	}
}
