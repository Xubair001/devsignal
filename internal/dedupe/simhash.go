// Package dedupe decides whether two postings are the same real-world job.
//
// The error costs are asymmetric and that shapes every threshold here. A MISSED
// merge shows the user the same job twice: annoying, self-evident, and they can
// see it. A FALSE merge hides a real job behind another company's posting: the
// user never learns the opportunity existed, and neither do we. Tuning therefore
// favours precision over recall, always.
package dedupe

import (
	"hash/fnv"
	"math/bits"
	"sort"
	"strings"
)

// ShingleSize is the word-gram width. 3 is large enough that boilerplate
// ("we are an equal opportunity employer") does not dominate the signature, and
// small enough to survive light editing between postings.
const ShingleSize = 3

// SimHash produces a 64-bit locality-sensitive signature: near-identical text
// yields signatures a small Hamming distance apart, which is what lets us
// compare within a block without an O(n^2) text diff.
func SimHash(text string) uint64 {
	shingles := Shingles(text)
	if len(shingles) == 0 {
		return 0
	}

	var weights [64]int
	for _, sh := range shingles {
		h := fnv.New64a()
		_, _ = h.Write([]byte(sh))
		sig := h.Sum64()
		for bit := 0; bit < 64; bit++ {
			if sig&(1<<uint(bit)) != 0 {
				weights[bit]++
			} else {
				weights[bit]--
			}
		}
	}

	var out uint64
	for bit := 0; bit < 64; bit++ {
		if weights[bit] > 0 {
			out |= 1 << uint(bit)
		}
	}
	return out
}

// Shingles tokenizes into overlapping word-grams. Deterministic and pure: the
// same text always yields the same signature, which is what makes a stored
// simhash comparable across runs and versions.
func Shingles(text string) []string {
	words := tokenize(text)
	if len(words) == 0 {
		return nil
	}
	if len(words) < ShingleSize {
		return []string{strings.Join(words, " ")}
	}
	out := make([]string, 0, len(words)-ShingleSize+1)
	for i := 0; i+ShingleSize <= len(words); i++ {
		out = append(out, strings.Join(words[i:i+ShingleSize], " "))
	}
	return out
}

// Hamming counts differing bits. Distance 0 means the signatures agree, which is
// strong but not conclusive evidence — hence the cascade in decide.go.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// tokenize lowercases, drops markup and keeps only alphanumeric words. HTML tags
// are stripped because two boards rendering the same job with different markup
// must produce the same signature.
func tokenize(text string) []string {
	text = stripTags(strings.ToLower(text))
	var words []string
	var cur strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r == '+' || r == '#':
			// Keep "c++" and "c#" intact: they are meaningful skill tokens.
			cur.WriteRune(r)
		default:
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
				b.WriteRune(' ')
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// BlockKey bounds the comparison set.
//
// Without blocking this is quadratic: 500K postings is 1.25e11 pairs, which will
// never run. Blocking on company plus the three most distinctive title tokens
// plus country means we only ever compare within small candidate sets.
func BlockKey(companyID, title, country string) string {
	toks := tokenize(title)
	toks = dropStopWords(toks)
	sort.Strings(toks)
	if len(toks) > 3 {
		toks = toks[:3]
	}
	return companyID + "|" + strings.Join(toks, "-") + "|" + strings.ToUpper(country)
}

// stopWords are the title filler that would otherwise dominate a sorted
// three-token key and put unrelated roles in the same block.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "the": true, "of": true, "for": true,
	"to": true, "in": true, "at": true, "with": true, "or": true,
}

func dropStopWords(in []string) []string {
	out := in[:0:0]
	for _, w := range in {
		if !stopWords[w] && len(w) > 1 {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return in
	}
	return out
}
