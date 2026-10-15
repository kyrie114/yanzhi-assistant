package db

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func NewID() string { return uuid.NewString() }

func UUID(s string) (pgtype.UUID, bool) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: [16]byte(u), Valid: true}, true
}

func MustUUID(s string) pgtype.UUID {
	id, ok := UUID(s)
	if !ok {
		return pgtype.UUID{}
	}
	return id
}

func UUIDs(ids []string) ([]pgtype.UUID, bool) {
	out := make([]pgtype.UUID, 0, len(ids))
	for _, s := range ids {
		id, ok := UUID(s)
		if !ok {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}
