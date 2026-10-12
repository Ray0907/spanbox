# Configure spanbox

spanbox is configured only through environment variables. Every one is optional; see [Environment variables](environment-variables.md) for the full table.

## Common setups

Local, open, loopback only:

```sh
./spanbox
```

Authenticated, custom port and data directory:

```sh
AUTH_TOKEN=$(openssl rand -hex 32) PORT=8080 DATA_DIR=/var/lib/spanbox ./spanbox
```

Keep traces for 7 days:

```sh
RETENTION_DAYS=7 ./spanbox
```

Keep traces forever:

```sh
RETENTION_DAYS=0 ./spanbox
```

Use your own price table and a regional Anthropic gateway:

```sh
PRICING_FILE=/etc/spanbox/prices.json ANTHROPIC_UPSTREAM=https://gateway.example.com ./spanbox
```

## Authentication mode

| | `AUTH_TOKEN` unset | `AUTH_TOKEN` set |
|---|---|---|
| Ingest | Open | Bearer token required |
| UI | Open | Login with the token |
| SQL console | Disabled | Login cookie only |
| Proxy | `/proxy/<vendor>/...` | `X-Spanbox-Token` header or `/proxy/t/<token>/...` |

Details are in [Run spanbox safely](security.md) and [HTTP routes](http-routes.md).

## Data directory

`DATA_DIR` holds `spanbox.db`. The database file is created with mode `0600`. On POSIX systems, spanbox warns when an existing data directory is accessible by group or other users. Create bind-mount directories first and restrict them, for example `mkdir -m 700 data`.

## Build version

```sh
go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" ./cmd/spanbox
```
