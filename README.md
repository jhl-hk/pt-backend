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

Returns prefix and routing path info for the given origin ASN.

```json
{
  "asn": 12345,
  "org": { "handle": "ORG-ACME-RIPE", "name": "ACME Corp" },
  "sponsor_org": { "handle": "ORG-SPONSOR-RIPE", "name": "Big ISP" },
  "prefixes": [
    { "prefix": "1.2.3.0/24", "path": [215172, 44324, 12345] }
  ]
}
```

- `org` — omitted if the ASN is not found in the RIPE DB
- `sponsor_org` — omitted if same as `org` or not present

## Database

The `asns` table is created automatically on startup (`IF NOT EXISTS`).

| Column         | Type        | Description                                      |
|----------------|-------------|--------------------------------------------------|
| `id`           | int (PK)    | ASN number                                       |
| `name`         | text        | Display name (manual)                            |
| `short`        | text        | Short name / abbreviation (manual)               |
| `org`          | text        | RIPE org handle                                  |
| `org_name`     | text        | RIPE org name                                    |
| `sponsor_org`  | text        | Sponsoring org handle                            |
| `sponsor_name` | text        | Sponsoring org name                              |
| `tags`         | text        | Comma-separated tag IDs, e.g. `"1,2,3"` (manual) |
| `comments`     | text        | Free-form notes (manual)                         |
| `whois`        | text        | Raw `aut-num` block from RIPE DB                 |
| `updated_at`   | timestamptz | Last sync time                                   |

Manual fields (`name`, `short`, `tags`, `comments`) are **never overwritten** by the RIPE DB sync.

If upgrading from a schema without `name`/`short`:

```sql
ALTER TABLE asns ADD COLUMN name text NOT NULL DEFAULT '';
ALTER TABLE asns ADD COLUMN short text NOT NULL DEFAULT '';
```
