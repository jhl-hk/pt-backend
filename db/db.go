package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Route mirrors bgp.PrefixAnalysis for persistent storage.
// The UNIQUE constraint on (prefix, peer_ip) means each (prefix, collector-peer)
// pair has exactly one current row — matching the in-memory store's structure.
type Route struct {
	bun.BaseModel `bun:"table:routes,alias:r"`

	ID             int64     `bun:"id,pk,autoincrement"`
	Prefix         string    `bun:"prefix,notnull"`
	PeerIP         string    `bun:"peer_ip,notnull"`
	PeerASN        uint32    `bun:"peer_asn,notnull"`
	OriginAS       uint32    `bun:"origin_as,notnull"`
	ASPath         []uint32  `bun:"as_path,array,notnull"`
	UpstreamChain  []uint32  `bun:"upstream_chain,array,notnull"`
	DirectUpstream uint32    `bun:"direct_upstream"`
	Tier1Found     uint32    `bun:"tier1_found"`
	Communities    []uint32  `bun:"communities,array"`
	NextHop        string    `bun:"next_hop"`
	Origin         string    `bun:"origin"`
	ReceivedAt     time.Time `bun:"received_at,notnull"`
}

// Database wraps a Bun DB connection.
type Database struct {
	DB *bun.DB
}

// NewDatabase opens a connection, pings, and auto-migrates the schema.
func NewDatabase(dsn string) (*Database, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	bunDB := bun.NewDB(sqldb, pgdialect.New())
	if err := bunDB.Ping(); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	d := &Database{DB: bunDB}
	if err := d.migrate(context.Background()); err != nil {
		return nil, fmt.Errorf("db migrate: %w", err)
	}
	return d, nil
}

func (d *Database) migrate(ctx context.Context) error {
	if _, err := d.DB.NewCreateTable().
		Model((*Route)(nil)).
		IfNotExists().
		Exec(ctx); err != nil {
		return err
	}
	indices := []string{
		`CREATE INDEX IF NOT EXISTS routes_origin_as ON routes(origin_as)`,
		`CREATE INDEX IF NOT EXISTS routes_prefix ON routes(prefix)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS routes_prefix_peer ON routes(prefix, peer_ip)`,
		`CREATE INDEX IF NOT EXISTS routes_as_path_gin ON routes USING GIN(as_path)`,
		`CREATE INDEX IF NOT EXISTS routes_upstream_chain_gin ON routes USING GIN(upstream_chain)`,
	}
	for _, idx := range indices {
		if _, err := d.DB.ExecContext(ctx, idx); err != nil {
			return err
		}
	}
	return nil
}

// BulkUpsert inserts or updates a batch of routes keyed on (prefix, peer_ip).
func (d *Database) BulkUpsert(ctx context.Context, routes []*Route) error {
	if len(routes) == 0 {
		return nil
	}
	_, err := d.DB.NewInsert().
		Model(&routes).
		On("CONFLICT (prefix, peer_ip) DO UPDATE").
		Set("peer_asn = EXCLUDED.peer_asn").
		Set("origin_as = EXCLUDED.origin_as").
		Set("as_path = EXCLUDED.as_path").
		Set("upstream_chain = EXCLUDED.upstream_chain").
		Set("direct_upstream = EXCLUDED.direct_upstream").
		Set("tier1_found = EXCLUDED.tier1_found").
		Set("communities = EXCLUDED.communities").
		Set("next_hop = EXCLUDED.next_hop").
		Set("origin = EXCLUDED.origin").
		Set("received_at = EXCLUDED.received_at").
		Exec(ctx)
	return err
}

// Delete removes a single route by (prefix, peer_ip).
func (d *Database) Delete(ctx context.Context, prefix, peerIP string) error {
	_, err := d.DB.NewDelete().
		Model((*Route)(nil)).
		Where("prefix = ? AND peer_ip = ?", prefix, peerIP).
		Exec(ctx)
	return err
}

// LoadAll returns every route in the DB for startup hydration.
func (d *Database) LoadAll(ctx context.Context) ([]*Route, error) {
	var routes []*Route
	err := d.DB.NewSelect().
		Model(&routes).
		OrderExpr("prefix, peer_ip").
		Scan(ctx)
	return routes, err
}
