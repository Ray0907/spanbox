# Durability with Litestream (plan)

**Goal:** a spanbox deployment survives losing its machine or disk without code changes in spanbox, using Litestream to stream the SQLite WAL to S3-compatible storage (or any path), and a documented, tested restore procedure.

**Scope:** documentation, a docker-compose recipe, a `litestream.yml`, and a repeatable verification script. No Go changes. Spec context: `docs/superpowers/specs/2026-09-15-spanbox-design.md` §5.4 (WAL, single writer, DB at `$DATA_DIR/spanbox.db`) and §18 (roadmap).

## Facts to respect

- spanbox opens the DB in WAL mode with `synchronous=NORMAL` and a single writer connection; Litestream requires WAL mode and works alongside a single writer. Litestream must NOT run `PRAGMA wal_checkpoint(TRUNCATE)` competing with spanbox in a way that breaks it; default Litestream checkpointing is compatible with WAL-mode apps.
- The DB path inside the container image is `/data/spanbox.db` (`ENV DATA_DIR=/data`). FTS5 and all tables live in the same file, so one replica covers everything.
- spanbox's retention job deletes rows and runs `incremental_vacuum`; that is ordinary WAL traffic for Litestream.
- Litestream replicates one database to one or more replicas: `s3://bucket/path`, `file:///path`, `abs://`, `gcs://`, `sftp://`. Verify the current Litestream version's config keys from its docs before writing `litestream.yml`; do not guess.

## Deliverables

1. `deploy/litestream/litestream.yml`

   ```yaml
   dbs:
     - path: /data/spanbox.db
       replicas:
         - type: s3
           bucket: ${LITESTREAM_BUCKET}
           path: spanbox
           endpoint: ${LITESTREAM_ENDPOINT}   # empty for AWS; set for MinIO/R2/B2
           access-key-id: ${LITESTREAM_ACCESS_KEY_ID}
           secret-access-key: ${LITESTREAM_SECRET_ACCESS_KEY}
           retention: 168h
           sync-interval: 1s
   ```

   Adjust key names to whatever the installed Litestream release actually accepts.

2. `deploy/litestream/docker-compose.yml`: two services sharing a named volume `spanbox-data`: `spanbox` (`ghcr.io/ray0907/spanbox:latest`, ports `4318:4318`, env `AUTH_TOKEN`) and `litestream` (`litestream/litestream:<pinned version>`, `replicate` command, config mounted read-only, env from `.env`). Include a commented `restore` one-liner:

   ```sh
   docker compose run --rm litestream restore -if-replica-exists -o /data/spanbox.db /data/spanbox.db
   ```

   and an `.env.example` with the four variables.

3. README section `## Durability with Litestream` under Configuration: three sentences on what it gives (RPO ≈ sync interval, restore to a new machine, no spanbox change), the compose recipe pointer, the restore command, and the two caveats: single writer only (do not run two spanbox instances on one restored file), and restore before starting spanbox on a fresh volume (spanbox creates an empty DB otherwise, and Litestream would then replicate the empty DB over the good replica; explain the `-if-replica-exists` guard and recommend an init container or the compose `depends_on` ordering).

4. `deploy/litestream/verify.sh`: an executable end-to-end check that runs on a developer laptop **without Docker**, using a `file://` replica:
   - build spanbox (`go build ./cmd/spanbox`), start it on a random free port with `AUTH_TOKEN=` and `DATA_DIR=$tmp/data`
   - start `litestream replicate $tmp/data/spanbox.db file://$tmp/replica`
   - ingest `internal/otlp/testdata/trace.json` via curl (JSON), wait 3 s
   - kill spanbox and litestream, delete `$tmp/data` entirely (simulated machine loss)
   - `litestream restore -o $tmp/data2/spanbox.db file://$tmp/replica`
   - start spanbox on `DATA_DIR=$tmp/data2`, query `GET /` and assert the trace name from the fixture appears; also `sqlite3 $tmp/data2/spanbox.db "PRAGMA integrity_check"` == `ok` and `SELECT count(*) FROM spans` == 3
   - print `PASS` / `FAIL` and exit accordingly; clean up processes with a trap
   Install Litestream for the check with `brew install litestream` if missing (say so in the script header; do not auto-install in the script, fail with a clear message instead).

5. Run `deploy/litestream/verify.sh` yourself and paste its output into `docs/superpowers/plans/2026-09-16-durability-litestream-result.md`, along with the Litestream version used and any config key you had to change from the sketch above.

## Constraints

- No Go changes, no new Go dependencies, `go vet ./... && go test ./...` still green.
- Conventional commits with trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Do not push.
- Docker daemon is not available on this machine: the compose file cannot be executed here; validate it with `docker compose config` only if the CLI accepts it without a daemon, otherwise note that it is untested and keep it minimal.
