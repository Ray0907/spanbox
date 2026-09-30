package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

// Use the existing 500k fixture, but restore the exact v1 indexes and compact
// away freed v2 pages. Otherwise the upgrade could reuse old v2 space and give
// a misleadingly low headroom measurement. Fixture preparation is not timed.
func upgradeV1(dir string, n int) {
	path := filepath.Join(dir, "spanbox.db")
	v1, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	must(err)
	v1.SetMaxOpenConns(1)
	_, err = v1.Exec(`DROP INDEX spans_kind; DROP INDEX spans_search;
		CREATE INDEX spans_kind ON spans(kind, start_ns);
		PRAGMA user_version=1; VACUUM;`)
	must(err)
	var version, free int
	must(v1.QueryRow("PRAGMA user_version").Scan(&version))
	must(v1.QueryRow("PRAGMA freelist_count").Scan(&free))
	check(version == 1 && free == 0, "upgrade fixture is not compact v1: version=%d free=%d", version, free)
	must(v1.Close())
	checkpoint(dir)

	// statvfs also observes allocated, unlinked SQLite sort files, unlike
	// directory enumeration. TMPDIR is on this same volume in load-e2e.sh.
	// Shared-volume activity can affect this sample; file-size peaks are also
	// reported independently. Python is already required by the shell driver.
	monitor := exec.Command("python3", "-c", `
import json, os, select, sys
from pathlib import Path
path = Path(sys.argv[1])
def size(suffix):
    try: return Path(str(path) + suffix).stat().st_size
    except FileNotFoundError: return 0
def available():
    s = os.statvfs(path.parent)
    return s.f_bavail * s.f_frsize
before = sum(size(s) for s in ('', '-wal', '-shm'))
base_free = available()
print(json.dumps({'DBBefore': size(''), 'BeforeFiles': before}), flush=True)
peak_files, peak_wal, peak_volume = before, 0, 0
while True:
    wal = size('-wal')
    peak_files = max(peak_files, size('') + wal + size('-shm'))
    peak_wal = max(peak_wal, wal)
    peak_volume = max(peak_volume, base_free - available())
    if select.select([sys.stdin], [], [], .005)[0]: break
print(json.dumps({'PeakFiles': peak_files, 'PeakWAL': peak_wal, 'PeakVolume': peak_volume}), flush=True)
`, path)
	monitor.Stderr = os.Stderr
	input, err := monitor.StdinPipe()
	must(err)
	output, err := monitor.StdoutPipe()
	must(err)
	must(monitor.Start())
	var space struct{ DBBefore, BeforeFiles, PeakFiles, PeakWAL, PeakVolume int64 }
	decoder := json.NewDecoder(output)
	must(decoder.Decode(&space)) // readiness: baseline sampled before Open
	start := time.Now()
	upgraded, openErr := store.Open(dir)
	elapsed := time.Since(start)
	must(input.Close())
	must(decoder.Decode(&space))
	must(monitor.Wait())
	must(openErr)
	defer upgraded.Close()
	must(upgraded.Reader().QueryRow("PRAGMA user_version").Scan(&version))
	check(version == 2, "500k upgrade version=%d, want 2", version)
	check(count(upgraded, "spans") == int64(n) && count(upgraded, "traces") == int64(n/5), "upgrade lost seeded rows")
	var indexes, matches int
	must(upgraded.Reader().QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='index' AND name IN ('spans_kind','spans_search')").Scan(&indexes))
	must(upgraded.Reader().QueryRow("SELECT count(*) FROM spans_fts WHERE spans_fts MATCH 'needle'").Scan(&matches))
	check(indexes == 2 && matches == n, "upgrade lost indexes/FTS: indexes=%d matches=%d", indexes, matches)
	check(space.PeakWAL > 0, "upgrade sampler did not observe migration WAL")
	mib := func(n int64) float64 { return float64(n) / (1 << 20) }
	after := size(path)
	fmt.Println("| Spans | v1 to v2 seconds | v1 DB MiB | v2 DB MiB | DB growth MiB | WAL peak MiB | Peak DB+WAL+SHM extra MiB | Peak volume extra MiB |")
	fmt.Println("|---:|---:|---:|---:|---:|---:|---:|---:|")
	fmt.Printf("| %d | %.3f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f |\n", n, elapsed.Seconds(), mib(space.DBBefore), mib(after), mib(after-space.DBBefore), mib(space.PeakWAL), mib(space.PeakFiles-space.BeforeFiles), mib(space.PeakVolume))
	fmt.Println("PASS compact 500k-span v1 startup upgrade: version, rows, indexes and full FTS matches preserved; disk/WAL sampled every 5ms")
}
