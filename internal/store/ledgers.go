package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/domain"
)

// InsertLedger inserts a ledger and fills in its creation time.
func InsertLedger(ctx context.Context, db DBTX, l *domain.Ledger) error {
	err := db.QueryRow(ctx,
		`INSERT INTO ledgers (id, name, metadata) VALUES ($1, $2, $3) RETURNING created_at`,
		l.ID, l.Name, nonNilMeta(l.Metadata),
	).Scan(&l.CreatedAt)
	return MapError(err)
}

// GetLedger loads a ledger by id.
func GetLedger(ctx context.Context, db DBTX, id uuid.UUID) (*domain.Ledger, error) {
	var l domain.Ledger
	err := db.QueryRow(ctx,
		`SELECT id, name, metadata, created_at FROM ledgers WHERE id = $1`, id,
	).Scan(&l.ID, &l.Name, &l.Metadata, &l.CreatedAt)
	if err != nil {
		return nil, MapError(err)
	}
	return &l, nil
}

// ListLedgers returns ledgers ordered newest first, after the given cursor.
func ListLedgers(ctx context.Context, db DBTX, ids []uuid.UUID, before *uuid.UUID, limit int) ([]domain.Ledger, error) {
	rows, err := db.Query(ctx, `
		SELECT id, name, metadata, created_at FROM ledgers
		 WHERE ($1::uuid[] IS NULL OR id = ANY($1))
		   AND ($2::uuid IS NULL OR id < $2)
		 ORDER BY id DESC
		 LIMIT $3`, ids, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Ledger
	for rows.Next() {
		var l domain.Ledger
		if err := rows.Scan(&l.ID, &l.Name, &l.Metadata, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func nonNilMeta(m domain.Metadata) domain.Metadata {
	if m == nil {
		return domain.Metadata{}
	}
	return m
}
