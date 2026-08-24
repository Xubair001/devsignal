package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

var (
	// ErrInvalidOutput means the model returned something the schema rejects.
	// The record fails into retry; it never writes partial skills.
	ErrInvalidOutput = errors.New("enrich: model output failed validation")
	// ErrEmptyInput guards against paying for a call with nothing to read.
	ErrEmptyInput = errors.New("enrich: nothing to extract from")
)

// Usage is what a call cost.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
}

// Raw is one provider response: the model's own words plus what it cost.
type Raw struct {
	JSON  []byte
	Usage Usage
	Model string
}

// Provider is the seam that keeps the model a configuration value rather than an
// architectural commitment (blueprint §17.2). Swapping tiers, or comparing them
// against the regression set, must not touch the pipeline.
type Provider interface {
	// Extract returns the model's raw JSON for one posting.
	Extract(ctx context.Context, text string) (Raw, error)
	// ModelID identifies the model in the cache key, so changing tier
	// invalidates cached extractions rather than mixing them.
	ModelID() string
}

// MinTextToExtract avoids paying for a call on a stub. A posting this short has
// nothing to extract, and the empty result would pollute the cache.
const MinTextToExtract = 200

// MaxTextToExtract truncates absurdly long descriptions. Job postings do not
// legitimately run past this, and an unbounded body is an unbounded bill.
const MaxTextToExtract = 60000

// ---------------------------------------------------------------- Claude

type ClaudeProvider struct {
	client anthropic.Client
	model  string
	// maxTokens is generous: a truncated response fails schema validation and
	// costs a full retry, which is more expensive than the headroom.
	maxTokens int64
}

type ClaudeConfig struct {
	APIKey string
	// Model is a config value. Default is the current flagship; changing it is a
	// deliberate act that invalidates the cache.
	Model     string
	MaxTokens int64
}

func NewClaudeProvider(cfg ClaudeConfig) (*ClaudeProvider, error) {
	if cfg.Model == "" {
		cfg.Model = "claude-opus-5"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	// With no explicit key the SDK resolves credentials itself (env var, then an
	// authenticated profile), so an unset key is not necessarily an error here.
	return &ClaudeProvider{
		client:    anthropic.NewClient(opts...),
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
	}, nil
}

func (p *ClaudeProvider) ModelID() string { return p.model }

func (p *ClaudeProvider) Extract(ctx context.Context, text string) (Raw, error) {
	text = strings.TrimSpace(text)
	if len(text) < MinTextToExtract {
		return Raw{}, ErrEmptyInput
	}
	if len(text) > MaxTextToExtract {
		text = text[:MaxTextToExtract]
	}

	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: p.maxTokens,
		// The stable prefix, cached. It is byte-identical on every call, which is
		// precisely what prompt caching requires — and why nothing volatile may
		// be added to it.
		System: []anthropic.TextBlockParam{{
			Text:         Instructions,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		// Schema-constrained output: a malformed shape is rejected by the API
		// rather than by our parser, so we never pay to re-run a bad parse.
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: JSONSchema()},
		},
		// The volatile part goes AFTER the cached breakpoint.
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(text)),
		},
	})
	if err != nil {
		return Raw{}, fmt.Errorf("enrich: model call: %w", err)
	}

	// A refusal is a successful HTTP response with no usable content. Checking
	// stop_reason before reading content is required, not defensive.
	if resp.StopReason == anthropic.StopReasonRefusal {
		return Raw{}, fmt.Errorf("%w: model declined (%s)", ErrInvalidOutput, resp.StopDetails.Category)
	}

	var body string
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			body += t.Text
		}
	}
	if strings.TrimSpace(body) == "" {
		return Raw{}, fmt.Errorf("%w: empty response", ErrInvalidOutput)
	}

	return Raw{
		JSON:  []byte(body),
		Model: p.model,
		Usage: Usage{
			InputTokens:     int(resp.Usage.InputTokens),
			OutputTokens:    int(resp.Usage.OutputTokens),
			CacheReadTokens: int(resp.Usage.CacheReadInputTokens),
		},
	}, nil
}

// ---------------------------------------------------------------- validation

// Validate parses and sanity-checks a provider response.
//
// The API already enforces the schema, so this catches the cases a schema
// cannot: contradictions between fields, and values that are structurally legal
// but semantically impossible.
func Validate(raw []byte) (Result, error) {
	var r Result
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidOutput, err)
	}

	// A stated salary with no text, or text with no claim, is a contradiction the
	// schema cannot express. Trusting either would put an unsupported salary
	// claim in front of a user.
	if r.SalaryStated && strings.TrimSpace(r.SalaryText) == "" {
		return Result{}, fmt.Errorf("%w: salary_stated with empty salary_text", ErrInvalidOutput)
	}
	if !r.SalaryStated && strings.TrimSpace(r.SalaryText) != "" {
		return Result{}, fmt.Errorf("%w: salary_text present but salary_stated is false", ErrInvalidOutput)
	}

	for i, s := range r.Skills {
		if strings.TrimSpace(s.Name) == "" {
			return Result{}, fmt.Errorf("%w: skill %d has an empty name", ErrInvalidOutput, i)
		}
		switch s.Level {
		case LevelRequired, LevelPreferred, LevelMentioned:
		default:
			return Result{}, fmt.Errorf("%w: skill %q has level %q", ErrInvalidOutput, s.Name, s.Level)
		}
	}
	if r.YearsExperience != nil && (*r.YearsExperience < 0 || *r.YearsExperience > 50) {
		return Result{}, fmt.Errorf("%w: years_experience_min out of range", ErrInvalidOutput)
	}
	return r, nil
}
