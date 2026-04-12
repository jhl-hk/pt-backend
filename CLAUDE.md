# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build -o pt-backend ./

# Run
go run main.go

# Test (no tests yet)
go test ./...

# Lint (requires golangci-lint)
golangci-lint run
```

The service expects `asns` and `output` data files in the current working directory when it starts.

## Architecture

**Player Tools Backend** is a BGP routing data aggregation microservice for AS215172 (MOEDOVE).

**Startup flow (main.go):**

1. SSH into remote BIRD router at `178.83.178.99`
2. Run `/etc/bird/update-moedove.sh` and fetch `/etc/bird/as_moedove_asns.conf`
3. Run `birdc s r all` to dump the full routing table
4. Parse local `asns` and `output` files
5. Build an in-memory ASN→prefixes index
6. Serve HTTP on `:8080`

**Data processing (handler/prefix.go):**

- `LoadASNs()` — parses BIRD-format ASN config into a set of known ASNs
- `ParsePrefixPaths()` — parses BIRD routing table dumps into `PrefixPath` structs
- `buildPath()` — the core path-filtering logic: deduplicates consecutive ASN prepending, collapses unknown transit ASNs
  to `[0, lastKnown]`, detects loops (stops if RootASN appears twice), and drops RootASN when followed by a Tier-1 ASN
- `IndexByASN()` — inverts the prefix list into an origin-ASN lookup map

**HTTP API (web/asn.go):**

- Single endpoint: `GET /api/v1/asn/{asn}`
- Returns `{"asn": int, "prefixes": [{"prefix": "...", "path": [...]}]}`
- Prefixes are sorted IPv4-before-IPv6, then numerically within each family

**Key constants (handler/prefix.go):**

- `RootASN = 215172` — the operator's own ASN
- `Tier1ASNs` — upstream providers (44324, 139317, 199310, 213605); paths ending at these after RootASN are simplified

**SSH auth:** uses hardware security key `~/.ssh/id_ed25519_sk_rk.pub`.
