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
	// ErrProviderUnavailable is a SYSTEMIC fault: missing credentials, an
	// authentication failure, a rate limit, or the provider being down. It fails
	// identically for every posting, so it must not consume any single posting's
	// retry budget.
	ErrProviderUnavailable = errors.New("enrich: provider unavailable")
)

// systemicMarkers identify faults that are about the provider or our
// configuration rather than about this document. Matched on text because the SDK
// surfaces credential resolution failures before any typed API error exists.
var systemicMarkers = []string{
	"no anthropic credentials",
	"authentication",
	"unauthorized",
	"invalid x-api-key",
	"permission",
	"rate limit",
	"overloaded",
	"too many requests",
	"connection refused",
	"no such host",
	"context deadline exceeded",
}

// ClassifyProviderError separates "the world is broken" from "this document is
// broken". Getting it wrong either burns the retry budget on a misconfiguration
// or gives up on a posting because of a transient blip.
//
// Applied by the Service rather than by each provider, so the policy is the same
// whatever is behind the interface — an earlier version classified inside the
// Claude provider only, which meant every other implementation silently bypassed
// it.
func ClassifyProviderError(err error) error {
	if err == nil {
		return nil
	}
	low := strings.ToLower(err.Error())
	for _, m := range systemicMarkers {
		if strings.Contains(low, m) {
			return fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
		}
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 401, apiErr.StatusCode == 403,
			apiErr.StatusCode == 429, apiErr.StatusCode >= 500:
			return fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
		}
	}
	return err
}

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

// ------------------------------------------------------- provider selection

// Provider names accepted by Resolve.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	// ProviderNone disables extraction. Named rather than implicit, so a
	// deployment that means to run without a model says so.
	ProviderNone = "none"
)

// ResolveConfig is everything Resolve needs to pick a provider.
type ResolveConfig struct {
	// Provider is explicit. Empty means infer from whichever key is present.
	Provider        string
	AnthropicAPIKey string
	OpenAIAPIKey    string
	// Model overrides the provider's default. Empty takes the default for
	// whichever provider was resolved, which is why the default cannot be set
	// in config: the right one depends on the answer.
	Model           string
	ReasoningEffort string
}

// DefaultAnthropicModel is the flagship. Changing it invalidates the cache.
const DefaultAnthropicModel = "claude-opus-5"

// ErrNoProvider says extraction is not configured.
//
// A distinct error rather than a nil provider: hard rule 7 says enrichment
// failure must not stop a posting reaching `ready` with a degraded quality flag,
// and the caller can only make that distinction if "no model configured" is
// separable from "the model call failed".
var ErrNoProvider = errors.New(
	"enrich: no extraction provider configured; set ANTHROPIC_API_KEY or " +
		"OPENAI_API_KEY, or set EXTRACTION_PROVIDER=none to disable extraction")

// Resolve picks a provider from configuration.
//
// Inference from a present key is what makes adding one line to .env sufficient,
// but an explicit EXTRACTION_PROVIDER always wins so a machine holding both keys
// is never ambiguous. Two keys and no choice is an ERROR rather than a
// precedence rule nobody remembers: which vendor read the postings is part of
// the extraction cache key and part of the audit trail, and picking it by
// alphabetical accident is not a decision anyone made.
func Resolve(cfg ResolveConfig) (Provider, error) {
	name := cfg.Provider
	if name == "" {
		switch {
		case cfg.AnthropicAPIKey != "" && cfg.OpenAIAPIKey != "":
			return nil, errors.New(
				"enrich: both ANTHROPIC_API_KEY and OPENAI_API_KEY are set; " +
					"set EXTRACTION_PROVIDER to anthropic or openai — which model read " +
					"a posting is part of its cache key and cannot be an accident")
		case cfg.AnthropicAPIKey != "":
			name = ProviderAnthropic
		case cfg.OpenAIAPIKey != "":
			name = ProviderOpenAI
		default:
			return nil, ErrNoProvider
		}
	}

	switch name {
	case ProviderNone:
		return nil, ErrNoProvider
	case ProviderAnthropic:
		model := cfg.Model
		if model == "" {
			model = DefaultAnthropicModel
		}
		if err := checkModelBelongsTo(ProviderAnthropic, model); err != nil {
			return nil, err
		}
		return NewClaudeProvider(ClaudeConfig{APIKey: cfg.AnthropicAPIKey, Model: model})
	case ProviderOpenAI:
		model := cfg.Model
		if model == "" {
			model = DefaultOpenAIModel
		}
		if err := checkModelBelongsTo(ProviderOpenAI, model); err != nil {
			return nil, err
		}
		return NewOpenAIProvider(OpenAIConfig{
			APIKey: cfg.OpenAIAPIKey, Model: model,
			ReasoningEffort: cfg.ReasoningEffort,
		})
	default:
		// Named explicitly rather than falling back to a default: a typo in a
		// deploy config must not silently change which model reads the corpus.
		return nil, fmt.Errorf("enrich: unknown EXTRACTION_PROVIDER %q (%s | %s | %s)",
			name, ProviderAnthropic, ProviderOpenAI, ProviderNone)
	}
}

// vendorPrefixes are model-name prefixes that unambiguously belong to one vendor.
var vendorPrefixes = map[string][]string{
	ProviderAnthropic: {"claude-"},
	ProviderOpenAI:    {"gpt-", "o1", "o3", "o4", "chatgpt-"},
}

// checkModelBelongsTo rejects a model name that plainly belongs to the other
// vendor.
//
// This exists because EXTRACTION_MODEL is a single variable shared by both
// providers, and the failure it prevents was a real one: a .env carrying
// EXTRACTION_MODEL=claude-opus-5 from the Anthropic default, plus a newly added
// OPENAI_API_KEY, resolved to "openai:claude-opus-5" and would have 400'd on
// every posting in the corpus.
//
// Deliberately only rejects the OTHER vendor's namespace rather than requiring a
// known name. A gateway or a fine-tune can be called anything, and a whitelist
// of model names is a file that goes stale the week after it is written — but
// "claude-" reaching OpenAI is never right.
func checkModelBelongsTo(provider, model string) error {
	for other, prefixes := range vendorPrefixes {
		if other == provider {
			continue
		}
		for _, pre := range prefixes {
			if strings.HasPrefix(model, pre) {
				return fmt.Errorf(
					"enrich: EXTRACTION_MODEL=%q is a %s model but the provider is %s; "+
						"set EXTRACTION_MODEL to a %s model or leave it empty for the default",
					model, other, provider, provider)
			}
		}
	}
	return nil
}
