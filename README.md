# Player Tools Backend

BGP routing data aggregation microservice for AS215172 (MOEDOVE).

## Environment

Create a `.env` file in the project root:

```dotenv
host=
user=
private_key=
DATABASE_URL=postgres://user:password@localhost:5432/dbname?sslmode=disable
```

## Prerequisites

- A running PostgreSQL instance
- SSH access to the BIRD router at the configured host
- `asns` and `output` data files (fetched automatically on startup)

## Build & Run

```bash
# Build
go build -o pt-backend ./

# Run
go run main.go
```

## API

### `GET /api/v1/asn/{asn}`

Basic AS metadata: name, country, org, relationships, and prefix count statistics. No prefix list.

```json
{
  "asn": 12345,
  "name": "ACME Network",
  "short": "ACME",
  "country": "DE",
  "website": "https://example.com",
  "tags": "1,2",
  "comments": "",
  "org": { "handle": "ORG-ACME-RIPE", "name": "ACME Corp" },
  "sponsor_org": { "handle": "ORG-SPONSOR-RIPE", "name": "Big ISP" },
  "relationships": { "peers": [...], "upstreams": [...], "downstreams": [...] },
  "prefix_count": 4,
  "v4_count": 3,
  "v6_count": 1,
  "v4_size": 2,
  "v6_size": 1
}
```

---

### `GET /api/v1/whois/{asn}`

Raw `aut-num` WHOIS block from RIPE DB, returned as `text/plain`.

---

### `GET /api/v1/cidr/{asn}`

Full prefix list with BGP paths for the given origin ASN.

```json
{
  "asn": 12345,
  "prefixes": [
    { "prefix": "1.2.3.0/24", "paths": [[215172, 44324, 12345]] },
    { "prefix": "2001:db8::/48", "paths": [[215172, 44324, 12345]] }
  ]
}
```

IPv4 prefixes are listed before IPv6, sorted numerically within each family.

---

### `GET /api/v1/tag/{tag}`

All ASNs that carry the given tag ID.

```json
{
  "tag": "1",
  "asns": [
    { "asn": 215172, "name": "MOEDOVE", "short": "MD", "country": "GB", "tags": "1,2" }
  ]
}
```

---

### `GET /api/v1/prefixes/count`

Total number of prefixes tracked.

```json
{ "prefix_count": 8192 }
```

---

### `GET /api/v1/rank/prefix`

Top 100 ASNs ranked by IPv4 and IPv6 prefix space (deduplicated, non-covered).

### `GET /api/v1/rank/downstream`

Top 100 ASNs by downstream customer count.

### `GET /api/v1/rank/peer`

Top 100 ASNs by peer count.

### `GET /api/v1/rank/ascone`

Top 100 ASNs by customer cone size.

## Database

The `asns` table is created automatically on startup (`IF NOT EXISTS`).

| Column         | Type        | Description                                      |
|----------------|-------------|--------------------------------------------------|
| `id`           | int (PK)    | ASN number                                       |
| `name`         | text        | Display name (manual)                            |
| `short`        | text        | Short name / abbreviation (manual)               |
| `country`      | text        | ISO 3166-1 alpha-2, synced from RIPE DB          |
| `website`      | text        | Website URL (manual)                             |
| `org`          | text        | RIPE org handle                                  |
| `org_name`     | text        | RIPE org name                                    |
| `sponsor_org`  | text        | Sponsoring org handle                            |
| `sponsor_name` | text        | Sponsoring org name                              |
| `tags`         | text        | Comma-separated tag IDs, e.g. `"1,2,3"` (manual) |
| `comments`     | text        | Free-form notes (manual)                         |
| `whois`        | text        | Raw `aut-num` block from RIPE DB                 |
| `updated_at`   | timestamptz | Last sync time                                   |

Manual fields (`name`, `short`, `website`, `tags`, `comments`) are **never overwritten** by the RIPE DB sync.
