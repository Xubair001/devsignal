package stages

import "testing"

// These helpers are small enough to look obviously correct and were not: an
// early derefOr returned the default in both branches, which silently dropped
// country out of every dedup block key and made blocks far larger than intended.
func TestPointerHelpers(t *testing.T) {
	v := "US"
	if got := derefOr(&v, "XX"); got != "US" {
		t.Errorf("derefOr(&\"US\") = %q, want US", got)
	}
	if got := derefOr(nil, "XX"); got != "XX" {
		t.Errorf("derefOr(nil) = %q, want XX", got)
	}
	if got := deref(&v); got != "US" {
		t.Errorf("deref = %q, want US", got)
	}
	if got := deref(nil); got != "" {
		t.Errorf("deref(nil) = %q, want empty", got)
	}
	if emptyToNil("") != nil {
		t.Error("emptyToNil(\"\") should be nil")
	}
	if p := emptyToNil("remote"); p == nil || *p != "remote" {
		t.Error("emptyToNil lost the value")
	}
}

func TestJoinScope(t *testing.T) {
	if joinScope(nil) != nil {
		t.Error("empty scope should be nil, not an empty string")
	}
	if p := joinScope([]string{"CA", "US"}); p == nil || *p != "CA,US" {
		t.Errorf("joinScope = %v, want CA,US", p)
	}
	if p := joinScope([]string{"US"}); p == nil || *p != "US" {
		t.Errorf("single scope = %v, want US", p)
	}
}

// Postgres has no uint64, so the signature is stored as int64. The BITS must
// survive the round trip; the magnitude is irrelevant to Hamming distance.
func TestSignedHashPreservesBits(t *testing.T) {
	for _, h := range []uint64{0, 1, 1 << 63, ^uint64(0), 0xDEADBEEFCAFEBABE} {
		got := signedHash(h)
		if got == nil {
			t.Fatal("nil hash")
		}
		if uint64(*got) != h {
			t.Errorf("bits lost: %#x -> %#x", h, uint64(*got))
		}
	}
}
