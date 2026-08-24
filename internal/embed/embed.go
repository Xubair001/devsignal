// Package embed turns text into a vector for candidate generation.
//
// PROVIDER CHOICE, stated plainly because it is a trade-off rather than an
// obvious answer:
//
// The default is a LOCAL, deterministic, lexical embedder. It is not a learned
// semantic model and does not pretend to be — it captures term overlap with some
// robustness to wording, which is enough to generate candidates but weaker than
// a hosted embedding model at understanding paraphrase.
//
// It is the default for three reasons. No job description or profile leaves our
// infrastructure, which matters because profiles are PII (blueprint §10.1). It
// costs nothing per call, and it is deterministic, so a vector never changes
// under a posting that did not change — the same property the extraction cache
// exists to protect.
//
// A hosted model drops in behind the Embedder interface when the eval harness
// (step 16) shows retrieval quality justifies the cost and the data egress. That
// is the point of measuring before buying rather than after.
package embed

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Dim is fixed by the column type: vector(768). Changing it is a migration, not
// a config change, which is why it is a constant rather than a setting.
const Dim = 768

var (
	ErrEmptyText = errors.New("embed: nothing to embed")
)

// Embedder produces a vector for text.
//
// ModelID and Version are stored on every vector row. Vectors from two different
// models are not comparable and similarity thresholds do not transfer between
// them, so a change to either must invalidate rather than mix (blueprint M-04).
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	ModelID() string
	Version() string
	Dim() int
}

// ---------------------------------------------------------------- local

// LocalModelID and LocalVersion identify the built-in embedder. Bump the version
// when the feature extraction changes, or old and new vectors silently coexist
// in one index and every similarity becomes meaningless.
const (
	LocalModelID = "local-hashed-lexical"
	LocalVersion = "v1"
)

// Local is a hashing (feature-hashing) embedder.
//
// Word unigrams and bigrams carry phrase-level signal; character 4-grams make it
// tolerant of the spelling and punctuation variation that job postings are full
// of ("Node.js" / "NodeJS", "front-end" / "frontend"). Term frequency is
// sublinear so a description that repeats one word twenty times does not drown
// out everything else, and the vector is L2-normalized so cosine similarity is a
// plain dot product.
type Local struct{}

func NewLocal() *Local { return &Local{} }

func (l *Local) ModelID() string { return LocalModelID }
func (l *Local) Version() string { return LocalVersion }
func (l *Local) Dim() int        { return Dim }

func (l *Local) Embed(_ context.Context, text string) ([]float32, error) {
	counts := features(text)
	if len(counts) == 0 {
		return nil, ErrEmptyText
	}

	vec := make([]float32, Dim)
	for feat, n := range counts {
		bucket, sign := bucketAndSign(feat)
		// Sublinear term frequency: repetition should add signal, not dominate.
		w := float32(1 + math.Log(float64(n)))
		vec[bucket] += sign * w
	}

	// L2 normalize so cosine similarity reduces to a dot product, and so two
	// documents of very different lengths remain comparable.
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return nil, ErrEmptyText
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
	return vec, nil
}

// features extracts the terms that make up the vector. Pure and deterministic:
// the same text always yields the same map, which is what makes a stored vector
// reproducible.
func features(text string) map[string]int {
	words := tokenize(text)
	if len(words) == 0 {
		return nil
	}
	out := make(map[string]int, len(words)*3)

	for i, w := range words {
		if !isStopWord(w) {
			out["u:"+w]++
		}
		// Bigrams keep phrase signal: "machine learning" is not the same as the
		// two words apart.
		if i+1 < len(words) {
			out["b:"+w+" "+words[i+1]]++
		}
		// Character 4-grams inside longer words absorb spelling variation.
		if len(w) >= 5 {
			for j := 0; j+4 <= len(w); j++ {
				out["c:"+w[j:j+4]]++
			}
		}
	}
	return out
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	inTag := false
	for _, r := range text {
		switch {
		// Job descriptions arrive as HTML; tags are noise that would otherwise
		// dominate the character n-grams.
		case r == '<':
			inTag = true
			flush()
		case r == '>':
			inTag = false
		case inTag:
			// skipped
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		case r == '+' || r == '#':
			// Kept: "c++" and "c#" are meaningful tokens, not punctuation.
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return words
}

// bucketAndSign hashes a feature to a dimension and a sign.
//
// The sign is what makes feature hashing work: without it, unrelated features
// colliding in a bucket always reinforce each other, so collisions manufacture
// similarity. With random signs they cancel in expectation instead.
func bucketAndSign(feat string) (int, float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(feat))
	sum := h.Sum64()
	bucket := int(sum % Dim)
	if sum&(1<<63) != 0 {
		return bucket, -1
	}
	return bucket, 1
}

// stopWords are dropped from unigrams only. They stay in bigrams, where they
// still carry structure ("years of experience").
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "for": true, "on": true, "at": true, "is": true,
	"are": true, "be": true, "with": true, "as": true, "by": true, "we": true,
	"you": true, "our": true, "your": true, "will": true, "that": true, "this": true,
}

func isStopWord(w string) bool { return len(w) < 2 || stopWords[w] }

// Cosine is the similarity between two normalized vectors. Exported because the
// retrieval layer and its tests both need it, and a second implementation would
// drift from this one.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(dot)
}
