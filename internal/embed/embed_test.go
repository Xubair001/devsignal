package embed

import (
	"context"
	"errors"
	"math"
	"testing"
)

var ctx = context.Background()

func mustEmbed(t *testing.T, e Embedder, text string) []float32 {
	t.Helper()
	v, err := e.Embed(ctx, text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	return v
}

const backendJob = "Senior Backend Engineer. You will design, build and operate Go " +
	"services backed by PostgreSQL, serving millions of requests per day. You will own " +
	"service reliability end to end, including on-call, capacity planning and incident " +
	"review, and mentor other backend engineers through design review."

const backendJobReworded = "Senior Server-Side Engineer. You will architect and run Go " +
	"services on PostgreSQL handling millions of requests daily. You own reliability " +
	"end to end: on-call, capacity planning, incident review, and mentoring backend " +
	"engineers in design review."

const frontendJob = "Senior Frontend Engineer. You will build accessible React interfaces " +
	"and own our design system, working closely with product designers on component " +
	"architecture, visual polish and browser performance across our web application."

const marketingJob = "Demand Generation Manager. You will own paid search, paid social " +
	"and lifecycle email, manage agency relationships and the media budget, and report " +
	"pipeline contribution to the executive team each quarter."

// A vector must never change under text that did not change: the same property
// the extraction cache protects, for the same reason.
func TestEmbeddingIsDeterministic(t *testing.T) {
	e := NewLocal()
	first := mustEmbed(t, e, backendJob)
	for i := 0; i < 5; i++ {
		again := mustEmbed(t, e, backendJob)
		if len(again) != len(first) {
			t.Fatalf("run %d: dimension changed", i)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("run %d: vector differs at dimension %d", i, j)
			}
		}
	}
}

func TestDimensionMatchesTheColumnType(t *testing.T) {
	e := NewLocal()
	if e.Dim() != Dim || Dim != 768 {
		t.Fatalf("Dim() = %d, const = %d; the column is vector(768)", e.Dim(), Dim)
	}
	if got := len(mustEmbed(t, e, backendJob)); got != 768 {
		t.Fatalf("produced %d dimensions, want 768", got)
	}
}

// Normalized vectors make cosine a dot product and keep documents of different
// lengths comparable.
func TestVectorsAreL2Normalized(t *testing.T) {
	e := NewLocal()
	for _, text := range []string{backendJob, frontendJob, "Go", "a b c d e f g"} {
		v, err := e.Embed(ctx, text)
		if err != nil {
			continue
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(math.Sqrt(sum)-1) > 1e-5 {
			t.Errorf("norm = %.6f for %q, want 1", math.Sqrt(sum), truncate(text))
		}
	}
	// Self-similarity of a normalized vector is exactly 1.
	v := mustEmbed(t, e, backendJob)
	if got := Cosine(v, v); math.Abs(float64(got)-1) > 1e-5 {
		t.Errorf("self-similarity = %.6f, want 1", got)
	}
}

// The property retrieval depends on: related text must rank above unrelated
// text. If this ordering does not hold, candidate generation is worthless
// regardless of how good the scorer is.
func TestRelatedTextRanksAboveUnrelated(t *testing.T) {
	e := NewLocal()
	base := mustEmbed(t, e, backendJob)

	same := Cosine(base, mustEmbed(t, e, backendJobReworded))
	sameField := Cosine(base, mustEmbed(t, e, frontendJob))
	unrelated := Cosine(base, mustEmbed(t, e, marketingJob))

	t.Logf("reworded=%.3f  other-engineering=%.3f  unrelated=%.3f", same, sameField, unrelated)

	if same <= sameField {
		t.Errorf("a reworded version of the same job (%.3f) did not rank above a "+
			"different engineering role (%.3f)", same, sameField)
	}
	if sameField <= unrelated {
		t.Errorf("another engineering role (%.3f) did not rank above an unrelated "+
			"role (%.3f)", sameField, unrelated)
	}
	if same < 0.3 {
		t.Errorf("reworded similarity %.3f is too low to retrieve a genuine match", same)
	}
}

// Signed hashing is what stops collisions manufacturing similarity. Without the
// sign, unrelated documents would drift together as the corpus grows.
func TestSignedHashingKeepsUnrelatedTextApart(t *testing.T) {
	e := NewLocal()
	unrelated := Cosine(mustEmbed(t, e, backendJob), mustEmbed(t, e, marketingJob))
	if unrelated > 0.5 {
		t.Errorf("unrelated documents scored %.3f; collisions are manufacturing similarity",
			unrelated)
	}
	// Signs must actually be mixed, or the mechanism is not doing anything.
	var pos, neg int
	for _, feat := range []string{"u:go", "u:react", "b:machine learning", "c:post", "u:kafka", "u:aws"} {
		if _, sign := bucketAndSign(feat); sign > 0 {
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		t.Errorf("hash signs are not mixed (%d positive, %d negative)", pos, neg)
	}
}

// HTML is noise that would otherwise dominate the character n-grams, since every
// posting arrives wrapped in the same tags.
func TestMarkupDoesNotDominate(t *testing.T) {
	e := NewLocal()
	plain := mustEmbed(t, e, backendJob)
	marked := mustEmbed(t, e,
		`<div class="content"><p>`+backendJob+`</p><ul><li>Benefits</li></ul></div>`)
	if got := Cosine(plain, marked); got < 0.9 {
		t.Errorf("markup changed the vector too much: cosine %.3f", got)
	}
}

func TestSkillTokensSurvive(t *testing.T) {
	e := NewLocal()
	cpp := mustEmbed(t, e, "Experience with C++ and systems programming required for this role")
	csharp := mustEmbed(t, e, "Experience with C# and .NET required for this role")
	// They share most words, so they will be close — but must not be identical,
	// or the language distinction is being thrown away.
	if got := Cosine(cpp, csharp); got > 0.98 {
		t.Errorf("C++ and C# postings are indistinguishable (cosine %.3f)", got)
	}
}

func TestEmptyAndTrivialInputIsRejected(t *testing.T) {
	e := NewLocal()
	for _, bad := range []string{"", "   ", "\n\t", "<div></div>", "a"} {
		if _, err := e.Embed(ctx, bad); !errors.Is(err, ErrEmptyText) {
			t.Errorf("Embed(%q) = %v, want ErrEmptyText", bad, err)
		}
	}
}

// Model and version are stored on every row; changing either must invalidate
// rather than mix, because thresholds do not transfer between models.
func TestModelAndVersionAreReported(t *testing.T) {
	e := NewLocal()
	if e.ModelID() == "" || e.Version() == "" {
		t.Fatal("embedder must identify itself for the version columns")
	}
	if e.ModelID() != LocalModelID || e.Version() != LocalVersion {
		t.Error("reported identity does not match the declared constants")
	}
}

func TestCosineHandlesMismatchedLengths(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{1, 0, 0}); got != 0 {
		t.Errorf("mismatched lengths returned %v, want 0 rather than a panic", got)
	}
}

func truncate(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "..."
}
