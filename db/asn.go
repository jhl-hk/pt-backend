package db

import (
	"context"
	"time"

	"github.com/uptrace/bun"
)

// ASNRecord maps to the `asns` table.
type ASNRecord struct {
	bun.BaseModel `bun:"table:asns"`

	ID          int       `bun:"id,pk"`
	Name        string    `bun:"name"`    // display name (manual)
	Short       string    `bun:"short"`   // short name / abbreviation (manual)
	Country     string    `bun:"country"` // ISO 3166-1 alpha-2, synced from RIPE DB
	Website     string    `bun:"website"` // manual
	Org         string    `bun:"org"`
	OrgName     string    `bun:"org_name"`
	SponsorOrg  string    `bun:"sponsor_org"`
	SponsorName string    `bun:"sponsor_name"`
	Tags        string    `bun:"tags"` // comma-separated, e.g. "1,2,3"
	Comments    string    `bun:"comments"`
	Whois       string    `bun:"whois"` // raw aut-num block from RIPE DB
	UpdatedAt   time.Time `bun:"updated_at"`
}

// CreateTable creates the asns table if it does not exist, and adds any
// missing columns for schema migrations.
func CreateTable(ctx context.Context, db *bun.DB) error {
	if _, err := db.NewCreateTable().
		Model((*ASNRecord)(nil)).
		IfNotExists().
		Exec(ctx); err != nil {
		return err
	}
	// Add new columns that may not exist in older deployments.
	migrations := []string{
		`ALTER TABLE asns ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE asns ADD COLUMN IF NOT EXISTS website TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := db.ExecContext(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// SeedASNs inserts new ASN rows but does nothing if the row already exists.
// Use this to ensure every known ASN has a row before org info is available.
func SeedASNs(ctx context.Context, db *bun.DB, records []ASNRecord) error {
	if len(records) == 0 {
		return nil
	}
	_, err := db.NewInsert().
		Model(&records).
		On("CONFLICT (id) DO NOTHING").
		Exec(ctx)
	return err
}

// UpsertASNs inserts or updates all records, preserving existing tags and comments.
func UpsertASNs(ctx context.Context, db *bun.DB, records []ASNRecord) error {
	if len(records) == 0 {
		return nil
	}
	_, err := db.NewInsert().
		Model(&records).
		On("CONFLICT (id) DO UPDATE").
		Set("country = EXCLUDED.country").
		Set("org = EXCLUDED.org").
		Set("org_name = EXCLUDED.org_name").
		Set("sponsor_org = EXCLUDED.sponsor_org").
		Set("sponsor_name = EXCLUDED.sponsor_name").
		Set("whois = EXCLUDED.whois").
		Set("updated_at = EXCLUDED.updated_at").
		// name, short, tags, comments, website are NOT overwritten — preserve manual edits
		Exec(ctx)
	return err
}
