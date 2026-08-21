package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

// Auditor appends to the tamper-evident audit log.
//
// The metadata field carries NO PII. This table deliberately outlives an
// erasure request, so anything identifying in it would defeat the erasure
// guarantee (see the privacy-surface skill).
type Auditor struct {
	q *store.Queries
}

func NewAuditor(q *store.Queries) *Auditor { return &Auditor{q: q} }

type Event struct {
	ActorID  *pgtype.UUID
	TenantID *pgtype.UUID
	Action   string
	Subject  string
	Metadata map[string]any
}

// Append chains the new entry onto the previous one. Must run inside a
// transaction: the advisory lock is transaction-scoped, and without it two
// writers can chain off the same predecessor.
func (a *Auditor) Append(ctx context.Context, ev Event) error {
	if err := a.q.LockAuditChain(ctx); err != nil {
		return fmt.Errorf("audit: lock: %w", err)
	}

	prev, err := a.q.GetLastAuditHash(ctx)
	if err != nil {
		// No rows means this is the first entry — a genesis link, not an error.
		prev = nil
	}

	meta := ev.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("audit: metadata: %w", err)
	}

	entry := chainHash(prev, ev.Action, ev.Subject, metaJSON)

	var actor, tenant pgtype.UUID
	if ev.ActorID != nil {
		actor = *ev.ActorID
	}
	if ev.TenantID != nil {
		tenant = *ev.TenantID
	}

	_, err = a.q.InsertAudit(ctx, store.InsertAuditParams{
		ActorID:   actor,
		TenantID:  tenant,
		Action:    ev.Action,
		Subject:   &ev.Subject,
		Metadata:  metaJSON,
		PrevHash:  prev,
		EntryHash: entry,
	})
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// chainHash binds each entry to its predecessor. Field lengths are included so
// that ("ab","c") and ("a","bc") cannot collide.
func chainHash(prev []byte, action, subject string, metadata []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	for _, f := range [][]byte{[]byte(action), []byte(subject), metadata} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write(f)
	}
	return h.Sum(nil)
}

// VerifyChain recomputes every link. Returns the id of the first bad entry, or 0.
func VerifyChain(entries []store.ListAuditForChainCheckRow, meta [][]byte, subjects []string) int64 {
	var prev []byte
	for i, e := range entries {
		want := chainHash(prev, e.Action, subjects[i], meta[i])
		if string(want) != string(e.EntryHash) {
			return e.ID
		}
		prev = e.EntryHash
	}
	return 0
}
