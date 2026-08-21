package profile

import "github.com/jackc/pgx/v5/pgtype"

// pgUUID adapts a path parameter into a pgtype.UUID without leaking pgtype into
// the handler signatures.
type pgUUID struct{ UUID pgtype.UUID }

func (p *pgUUID) Scan(s string) error { return p.UUID.Scan(s) }
