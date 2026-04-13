package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/uptrace/bun"
)

// PrefixRecord maps to the `prefixes` table.
type PrefixRecord struct {
	bun.BaseModel `bun:"table:prefixes"`

	Prefix    string          `bun:"prefix,pk"`
	OriginASN int             `bun:"origin_asn,notnull"`
	Paths     json.RawMessage `bun:"paths,type:jsonb,notnull"`
	UpdatedAt time.Time       `bun:"updated_at,notnull"`
}

// CreatePrefixTable creates the prefixes table if it does not exist.
func CreatePrefixTable(ctx context.Context, db *bun.DB) error {
	_, err := db.NewCreateTable().
		Model((*PrefixRecord)(nil)).
		IfNotExists().
		Exec(ctx)
	return err
}

// UpsertPrefixes inserts or replaces all prefix records.
func UpsertPrefixes(ctx context.Context, db *bun.DB, records []PrefixRecord) error {
	if len(records) == 0 {
		return nil
	}
	_, err := db.NewInsert().
		Model(&records).
		On("CONFLICT (prefix) DO UPDATE").
		Set("origin_asn = EXCLUDED.origin_asn").
		Set("paths = EXCLUDED.paths").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

// CountPrefixes returns the total number of prefix rows.
func CountPrefixes(ctx context.Context, db *bun.DB) (int, error) {
	return db.NewSelect().Model((*PrefixRecord)(nil)).Count(ctx)
}

// LoadAllPrefixes returns all rows from the prefixes table.
func LoadAllPrefixes(ctx context.Context, db *bun.DB) ([]PrefixRecord, error) {
	var records []PrefixRecord
	err := db.NewSelect().Model(&records).Scan(ctx)
	return records, err
}

// LoadAllASNIDs returns all ASN IDs from the asns table.
func LoadAllASNIDs(ctx context.Context, db *bun.DB) ([]int, error) {
	var ids []int
	err := db.NewSelect().TableExpr("asns").Column("id").Scan(ctx, &ids)
	return ids, err
}
