package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------- OpenAI

// OpenAIProvider extracts through OpenAI's Chat Completions API.
//
// Raw HTTP rather than the vendor SDK, deliberately. Hard rule 4 specifies the
// client shape this system requires — four separate timeouts, a bounded
// connection pool, and every body read through an io.LimitReader — and the
// surface here is a single POST to a single endpoint. Vendoring an SDK to send
// one request would put that shape behind someone else's defaults.
//
// The schema is shared with the Claude provider, unmodified. Verified against
// the live API: OpenAI's strict mode accepts it as written, including the
// maxLength, maxItems and integer-or-null constraints, so there is no second
// schema to keep in step and no divergence for the two providers to drift into.
type OpenAIProvider struct {
	http    *http.Client
	key     string
	model   string
	effort  string
	baseURL string
	maxOut  int
}

// OpenAIConfig configures the provider.
type OpenAIConfig struct {
	APIKey string
	// Model is a config value and part of the extraction cache key, so changing
	// it invalidates cached extractions rather than mixing two models' output.
	Model string
	// ReasoningEffort trades latency and output tokens for depth.
	//
	// Defaults to "minimal", which is a measured choice rather than a cautious
	// one. On a real 2.2 KB posting, gpt-5-mini at minimal effort returned 12
	// skills using 280 output tokens in 5.2s; at default effort it spent 2,531
	// output tokens and 38s to return ELEVEN. Extraction is a mechanical reading
	// task, and paying a reasoning budget on it bought a worse answer.
	ReasoningEffort string
	MaxOutputTokens int
	// BaseURL exists for tests and for an OpenAI-compatible gateway. Empty means
	// the real API.
	BaseURL string
	Timeout time.Duration
}

// ReasoningEffortMinimal is the default. See OpenAIConfig.ReasoningEffort.
const ReasoningEffortMinimal = "minimal"

// DefaultOpenAIModel is the cheapest model measured to extract well here.
const DefaultOpenAIModel = "gpt-5-mini"

// NewOpenAIProvider builds one.
func NewOpenAIProvider(cfg OpenAIConfig) (*OpenAIProvider, error) {
	if cfg.APIKey == "" {
		// Unlike the Anthropic SDK, there is no ambient credential chain to fall
		// back to here, so an empty key is an error rather than a maybe. Failing
		// at construction beats failing once per posting.
		return nil, errors.New("enrich: OPENAI_API_KEY is required for the openai provider")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultOpenAIModel
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = ReasoningEffortMinimal
	}
	if cfg.MaxOutputTokens == 0 {
		// Generous: a truncated response fails schema validation and costs a full
		// retry, which is more expensive than the headroom.
		cfg.MaxOutputTokens = 8192
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Timeout == 0 {
		// Wider than a source fetch. A reasoning model can legitimately take tens
		// of seconds, and measured latency here ran to 38s at default effort.
		cfg.Timeout = 120 * time.Second
	}

	// Hard rule 4: never http.DefaultClient. Four timeouts because they fail
	// differently — a dial timeout is a dead host, a TLS timeout a broken
	// middlebox, a response-header timeout a hung server, and the total timeout
	// is the only defence against a slow drip.
	tr := &http.Transport{
		MaxConnsPerHost:       8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &OpenAIProvider{
		http:    &http.Client{Transport: tr, Timeout: cfg.Timeout},
		key:     cfg.APIKey,
		model:   cfg.Model,
		effort:  cfg.ReasoningEffort,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		maxOut:  cfg.MaxOutputTokens,
	}, nil
}

// ModelID identifies the model in the cache key.
//
// Prefixed, so a cached extraction from one vendor can never be read as the
// other's. Two models can share a name across providers, and hard rule 8 makes
// this string part of the determinism guarantee — an ambiguous id would let a
// provider switch silently reuse the wrong output.
func (p *OpenAIProvider) ModelID() string { return "openai:" + p.model }

// maxResponseBytes caps the response body. Untrusted remote content read
// without a bound is the most reliable way to kill a Go service, and that holds
// for a paid API as much as for a scraped page.
const maxResponseBytes = 4 << 20

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	// ReasoningEffort is omitted for models that do not accept it; sending it to
	// a non-reasoning model is a 400.
	ReasoningEffort     string               `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int                  `json:"max_completion_tokens,omitempty"`
	ResponseFormat      openAIResponseFormat `json:"response_format"`
}

type openAIResponseFormat struct {
	Type       string           `json:"type"`
	JSONSchema openAIJSONSchema `json:"json_schema"`
}

type openAIJSONSchema struct {
	Name string `json:"name"`
	// Strict: a malformed shape is rejected by the API rather than by our
	// parser, so we never pay to re-run a bad parse.
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
			// Refusal is a successful HTTP response with no usable content.
			// Checking it before reading content is required, not defensive.
			Refusal *string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		PromptDetails    struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// supportsReasoningEffort reports whether the model accepts the parameter.
//
// A allow-list on the family prefix rather than a try-and-retry: a 400 costs a
// round trip on every single posting, and the families are few and stable.
func supportsReasoningEffort(model string) bool {
	for _, p := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// Extract returns the model's raw JSON for one posting.
func (p *OpenAIProvider) Extract(ctx context.Context, text string) (Raw, error) {
	text = strings.TrimSpace(text)
	if len(text) < MinTextToExtract {
		return Raw{}, ErrEmptyInput
	}
	if len(text) > MaxTextToExtract {
		text = text[:MaxTextToExtract]
	}

	req := openAIRequest{
		Model: p.model,
		Messages: []openAIMessage{
			// Instructions is the byte-identical stable prefix shared with the
			// Claude provider. OpenAI caches prompt prefixes automatically at 1024
			// tokens and up, which this does not reach on its own — so the
			// content-hash cache in this package, not the vendor's, is what makes
			// re-extraction free. That is hard rule 8, and it is provider-neutral
			// by design.
			{Role: "system", Content: Instructions},
			{Role: "user", Content: text},
		},
		MaxCompletionTokens: p.maxOut,
		ResponseFormat: openAIResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIJSONSchema{
				Name: "posting_extraction", Strict: true, Schema: JSONSchema(),
			},
		},
	}
	if supportsReasoningEffort(p.model) {
		req.ReasoningEffort = p.effort
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Raw{}, fmt.Errorf("enrich: encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Raw{}, fmt.Errorf("enrich: building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.key)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return Raw{}, fmt.Errorf("enrich: model call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Raw{}, fmt.Errorf("enrich: reading response: %w", err)
	}
	if int64(len(raw)) > maxResponseBytes {
		return Raw{}, fmt.Errorf("%w: response exceeded %d bytes",
			ErrInvalidOutput, maxResponseBytes)
	}

	var out openAIResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		// The status is included but the body is NOT: an error body can echo the
		// request, and the request contains the posting text.
		return Raw{}, fmt.Errorf("enrich: decoding response (status %d): %w",
			resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := "no message"
		if out.Error != nil {
			msg = out.Error.Type + ": " + out.Error.Message
		}
		return Raw{}, fmt.Errorf("enrich: model call: status %d: %s", resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return Raw{}, fmt.Errorf("%w: no choices returned", ErrInvalidOutput)
	}
	c := out.Choices[0]
	if c.Message.Refusal != nil && *c.Message.Refusal != "" {
		return Raw{}, fmt.Errorf("%w: model declined", ErrInvalidOutput)
	}
	if c.FinishReason == "length" {
		// Distinguished from a malformed body: a truncated response means the
		// token ceiling is too low, which is a configuration problem, not a model
		// one. Reported so it is fixable rather than looking like flakiness.
		return Raw{}, fmt.Errorf("%w: response truncated at %d output tokens",
			ErrInvalidOutput, p.maxOut)
	}
	if strings.TrimSpace(c.Message.Content) == "" {
		return Raw{}, fmt.Errorf("%w: empty response", ErrInvalidOutput)
	}

	return Raw{
		JSON:  []byte(c.Message.Content),
		Model: p.ModelID(),
		Usage: Usage{
			InputTokens:     out.Usage.PromptTokens,
			OutputTokens:    out.Usage.CompletionTokens,
			CacheReadTokens: out.Usage.PromptDetails.CachedTokens,
		},
	}, nil
}
