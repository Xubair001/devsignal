package ingest

import (
	"encoding/json"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/source"
)

func decodeCursor(b []byte) source.Cursor {
	var c source.Cursor
	if len(b) == 0 {
		return c
	}
	// A malformed cursor must degrade to a full fetch, never abort the poll.
	_ = json.Unmarshal(b, &c)
	return c
}

func encodeCursor(c source.Cursor) []byte {
	b, err := json.Marshal(c)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func numeric(f float64) pgtype.Numeric {
	// parse_yield_7d is numeric(5,4): store as an integer scaled by 1e4.
	return pgtype.Numeric{Int: big.NewInt(int64(f * 10000)), Exp: -4, Valid: true}
}

func strptr(s string) *string { return &s }
