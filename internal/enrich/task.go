package enrich

// Task is what to ask a model for.
//
// The provider was posting-specific: the instructions and schema were
// package-level constants baked into Extract. That was fine while a posting was
// the only thing we read, and wrong the moment a resume needed reading too —
// the alternative was a second provider pair with its own HTTP client, timeouts
// and error classification, all duplicated.
//
// Every field here is part of a cache key somewhere, which is why the versions
// travel with the prompt rather than living beside it: hard rule 8 makes
// (content_hash, prompt_version, model_id, schema_version) the determinism
// guarantee, and a prompt that changed without its version would silently reuse
// output produced by different instructions.
type Task struct {
	// Name identifies the schema to the provider. OpenAI requires one; Anthropic
	// ignores it.
	Name string
	// Instructions is the stable prefix. It must be byte-identical on every call
	// of this task or prompt caching silently stops working, so nothing volatile
	// may appear in it.
	Instructions string
	// Schema constrains the response. Shared shape across providers — verified
	// that OpenAI's strict mode accepts the same map Anthropic does.
	Schema func() map[string]any
	// PromptVersion and SchemaVersion are cache-key components.
	PromptVersion string
	SchemaVersion string
	// MaxOutputTokens overrides the provider default. Zero means the default.
	MaxOutputTokens int
}

// PostingTask reads a job posting. The original behaviour of Extract.
func PostingTask() Task {
	return Task{
		Name:          "posting_extraction",
		Instructions:  Instructions,
		Schema:        JSONSchema,
		PromptVersion: PromptVersion,
		SchemaVersion: SchemaVersion,
	}
}
