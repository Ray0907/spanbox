# Retention and Upgrades

## Retention

spanbox deletes complete traces older than `RETENTION_DAYS` (default 30; `0` disables). A trace that still contains unfinished spans or recent arrivals is never deleted.

The worker deletes in small batches to protect ingest, and periodically finishes FTS merge generations to retire pending delete postings:

| Variable | Default | Range |
|---|---:|---|
| `PURGE_BATCH_SIZE` | 10 | 1–200 |
| `FTS_MERGE_EVERY` | 256 | 1–1024 |

On the 500k-span load fixture the defaults keep `/search` p95 within 2x baseline (150 ms baseline, 209 ms during purge) and ingest p95 within 3x. A purge takes about 68 s and the database shrinks from 1212 to 608 MiB.

Writer fairness is judged on whole-purge p95, not a hard maximum. A large common FTS term can still cause a long single ingest. The tuning ranges are safety caps, not performance guarantees. Benchmark representative data rather than raising batch size blindly.

### Import and errors

- Retention skips during an active import, logs it, retries one minute later, then returns to hourly sweeps.
- On a batch, merge, or checkpoint error, retention attempts FTS finalization and reclamation once with a 30 s timeout, preserving the original error. Canceling the purge context skips that cleanup.

### Shrinking legacy databases

Legacy databases without incremental auto-vacuum still delete expired traces, but skip vacuum with one warning per opened store. To enable shrinkage, stop spanbox, back up the database, then:

```sh
sqlite3 "${DATA_DIR:-./data}/spanbox.db" 'PRAGMA auto_vacuum=INCREMENTAL; VACUUM;'
```

Offline `VACUUM` needs its own temporary-copy space.

### Load testing

```sh
go run ./scripts/load -work <temporary-directory> -binary <absolute-path-to-spanbox>
```

`-sweep` tries batch sizes 10/50/200 and merge intervals 1/256. `-merge-intervals 256` narrows the sweep and `-purge-timeout 2m` rejects slow candidates sooner. `scripts/load-e2e.sh` forwards these flags, but its final report step reads a result file that is absent at this HEAD and will fail.

## Upgrades

Back up `spanbox.db` before upgrading. The schema v1→v2 upgrade builds dashboard and search indexes before the HTTP server starts.

On the 500k-span fixture (about 1.3 KiB input and output per span) the migration took 1.384 s. The database grew from 1,180.19 to 1,211.83 MiB, WAL peaked at 41.98 MiB, and peak additional DB/WAL/SHM space was 73.71 MiB.

Keep at least 512 MiB extra free space on the data filesystem and on the SQLite temporary-sort filesystem (512 MiB total if shared), in addition to backup space. This is a planning allowance, not an upper bound: increase it for more spans or longer model names, and allow startup probes time to finish.
