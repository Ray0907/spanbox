// Opt-in integration/load driver, invoked by scripts/load-e2e.sh, not go test.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
	"github.com/Ray0907/spanbox/internal/web"
)

const token = "load-e2e-secret"

var client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func login(base string) {
	jar, err := cookiejar.New(nil)
	must(err)
	client.Jar = jar
	resp, err := client.Post(base+"/login", "application/x-www-form-urlencoded", strings.NewReader("token="+token))
	must(err)
	defer resp.Body.Close()
	check(resp.StatusCode == http.StatusFound, "UI login failed: HTTP %d", resp.StatusCode)
}

var failures []string

func check(ok bool, format string, args ...any) {
	if !ok {
		failures = append(failures, fmt.Sprintf(format, args...))
		fmt.Printf("FAIL: "+format+"\n", args...)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func id(n int) string { return fmt.Sprintf("%032x", n+1) }

func seed(db *store.Store, count int, now time.Time) {
	old := now.Add(-70 * 24 * time.Hour).UnixNano()
	recent := now.Add(-time.Hour).UnixNano()
	cutoff := now.Add(-30 * 24 * time.Hour).UnixNano()
	tokens, cost := int64(10), 0.00001
	body := strings.Repeat("needle searchable prompt response content ", 16)
	for offset := 0; offset < count; offset += 1000 {
		spans := make([]store.Span, 0, 1000)
		for i := offset; i < min(offset+1000, count); i++ {
			tid, child := i/5, i%5
			start := recent + int64(i)*1000000
			if tid < count/10 {
				start = old + int64(i)*1000000
			}
			end := start + int64(child+1)*1000000
			// Five protected traces: mixed starts, recent end, unfinished,
			// end exactly at cutoff, and start exactly at cutoff.
			if child == 4 {
				switch tid {
				case 0:
					start, end = recent, recent+1000000
				case 1:
					end = recent
				case 2:
					end = 0
				case 3:
					end = cutoff
				case 4:
					start, end = cutoff, cutoff+1000000
				}
			}
			parent := ""
			if child != 0 {
				parent = fmt.Sprintf("%016x", tid*5+1)
			}
			spans = append(spans, store.Span{TraceID: id(tid), SpanID: fmt.Sprintf("%016x", i+1),
				ParentSpanID: parent, Name: "load span", Kind: "llm", ServiceName: "load-service",
				StartNs: start, EndNs: end, DurationMs: float64(child + 1),
				RequestModel: fmt.Sprintf("load-model-%d", tid%4), SessionID: fmt.Sprintf("session-%06d", tid/10),
				InputTokens: &tokens, OutputTokens: &tokens, CostUSD: &cost,
				InputContent: body, OutputContent: body, Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}"})
		}
		must(db.InsertBatch(context.Background(), spans))
	}
}

func request(base, path string, body []byte) ([]byte, time.Duration, error) {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Since(start), err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	duration := time.Since(start)
	if err == nil && (resp.StatusCode != 200 || bytes.Contains(data, []byte(`class="error"`)) || bytes.Contains(data, []byte("SQLITE_BUSY"))) {
		err = fmt.Errorf("%s: HTTP %d: %.300s", path, resp.StatusCode, data)
	}
	if err == nil && body == nil {
		switch {
		case strings.HasPrefix(path, "/dashboard/data"):
			var dashboard store.DashboardData
			err = json.Unmarshal(data, &dashboard)
			if err == nil && (dashboard.TraceCount == 0 || len(dashboard.Models) == 0) {
				err = fmt.Errorf("empty dashboard")
			}
		case strings.HasPrefix(path, "/sessions"):
			if !bytes.Contains(data, []byte("session-")) {
				err = fmt.Errorf("empty sessions")
			}
		case strings.HasPrefix(path, "/search"):
			if !bytes.Contains(data, []byte("load span")) {
				err = fmt.Errorf("empty search")
			}
		default:
			if path != "/healthz" && !bytes.Contains(data, []byte("load ")) {
				err = fmt.Errorf("empty trace read")
			}
		}
	}
	return data, duration, err
}

func stats(values []time.Duration) [3]float64 {
	if len(values) == 0 {
		return [3]float64{}
	}
	copy := append([]time.Duration(nil), values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return [3]float64{float64(copy[int(math.Ceil(float64(len(copy))*.5))-1]) / 1e6,
		float64(copy[int(math.Ceil(float64(len(copy))*.95))-1]) / 1e6, float64(copy[len(copy)-1]) / 1e6}
}

type samples struct {
	reads, ingest []time.Duration
	readByPath    map[string][]time.Duration
	searchWindows map[int][]time.Duration
	ingestWindows map[int][]time.Duration
	errors        []error
}

// Same closed-loop traffic in both phases: 2 read workers (all five pages),
// 1 real OTLP ingest worker (one new span per request); 20ms between requests.
func traffic(base string, paths []string, stop <-chan struct{}, serial *atomic.Int64) samples {
	result := samples{readByPath: make(map[string][]time.Duration), searchWindows: make(map[int][]time.Duration), ingestWindows: make(map[int][]time.Duration)}
	started := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Go(func() {
			for turn := 0; ; turn++ {
				select {
				case <-stop:
					return
				default:
				}
				path := paths[(turn+worker)%len(paths)]
				var body []byte
				if worker == 2 {
					path = "/v1/traces"
					n := serial.Add(1) + 1000000
					now := time.Now().UnixNano()
					body = []byte(fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"%032x","spanId":"%016x","name":"load ingest","startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`, n, n, now, now+1000000))
				}
				_, duration, err := request(base, path, body)
				mu.Lock()
				if worker == 2 {
					result.ingest = append(result.ingest, duration)
					window := int(time.Since(started) / (5 * time.Second))
					result.ingestWindows[window] = append(result.ingestWindows[window], duration)
				} else {
					result.reads = append(result.reads, duration)
					result.readByPath[path] = append(result.readByPath[path], duration)
					if strings.HasPrefix(path, "/search") {
						window := int(time.Since(started) / (5 * time.Second))
						result.searchWindows[window] = append(result.searchWindows[window], duration)
					}
				}
				if err != nil {
					result.errors = append(result.errors, err)
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	wg.Wait()
	return result
}

func size(path string) int64 {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	must(err)
	return info.Size()
}

func checkpoint(dir string) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "spanbox.db")+"?_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	var busy, log, done int
	must(db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done))
	must(func() error {
		if busy != 0 {
			return fmt.Errorf("checkpoint busy: %d/%d", done, log)
		}
		return nil
	}())
}

func count(db *store.Store, table string) int64 {
	var n int64
	must(db.Reader().QueryRow("SELECT count(*) FROM " + table).Scan(&n))
	return n
}

func disabled(binary, dir string, expected int) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	port := listener.Addr().(*net.TCPAddr).Port
	must(listener.Close())
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "PORT="+fmt.Sprint(port), "DATA_DIR="+dir, "RETENTION_DAYS=0", "AUTH_TOKEN="+token)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	must(cmd.Start())
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		must(cmd.Wait())
		check(!strings.Contains(logs.String(), "retention:"), "disabled retention ran: %s", logs.String())
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if _, _, err := request(base, "/healthz", nil); err == nil {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	check(ready, "production binary did not start")
	time.Sleep(2 * time.Second)
	db, err := store.Open(dir)
	must(err)
	check(count(db, "spans") == int64(expected), "RETENTION_DAYS=0 removed old spans")
	must(db.Close())
	fmt.Println("PASS RETENTION_DAYS=0: real production binary retained all aged spans")
}

func prepare(work, binary string, n int) (string, time.Time) {
	dir := filepath.Join(work, fmt.Sprintf("seed-%d", n))
	db, err := store.Open(dir)
	must(err)
	now := time.Now().UTC()
	start := time.Now()
	seed(db, n, now)
	fmt.Printf("Seeded %d spans / %d traces (5 spans/trace, 10 traces/session, 1.3KiB bodies; half >60 days) in %s\n", n, n/5, time.Since(start).Round(time.Millisecond))
	must(db.Close())
	if n == 500000 {
		upgradeV1(dir, n)
	}
	disabled(binary, dir, n)
	checkpoint(dir)
	return dir, now
}

type measurement struct {
	spans    int
	options  store.PurgeOptions
	search   [3]float64
	ingest   [3]float64 // baseline p95, purge p95, purge MAX (informational)
	failures int
}

func run(work, source string, now time.Time, n int, options store.PurgeOptions, purgeTimeout time.Duration) measurement {
	previousFailures := len(failures)
	dir := filepath.Join(work, fmt.Sprintf("%d-batch%d-merge%d", n, options.BatchSize, options.MergeEvery))
	must(os.MkdirAll(dir, 0700))
	defer os.RemoveAll(dir)
	// Each combination starts from the same unpurged, checkpointed fixture.
	input, err := os.Open(filepath.Join(source, "spanbox.db"))
	must(err)
	output, err := os.OpenFile(filepath.Join(dir, "spanbox.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	_, err = io.Copy(output, input)
	must(err)
	must(input.Close())
	must(output.Close())
	db, err := store.Open(dir)
	must(err)
	defer db.Close()
	fmt.Printf("\nDataset %d, batch=%d traces, explicit merge every=%d batches\n", n, options.BatchSize, options.MergeEvery)
	prices, err := pricing.Load("")
	must(err)
	server := httptest.NewServer(web.NewHandler(web.Deps{Cfg: config.Config{AuthToken: token}, Store: db, Pricing: prices}))
	defer server.Close()
	login(server.URL)
	paths := []string{"/", "/sessions", "/search?q=needle", "/dashboard/data", "/traces/" + id(n/5-1)}
	fmt.Println("| Spans | Endpoint | First ms | p50 ms | p95 ms | max ms |")
	fmt.Println("|---:|---|---:|---:|---:|---:|")
	for _, path := range paths {
		var values []time.Duration
		for i := 0; i < 5; i++ {
			_, duration, err := request(server.URL, path, nil)
			check(err == nil, "%d %s: %v", n, path, err)
			values = append(values, duration)
		}
		s := stats(values)
		fmt.Printf("| %d | %s | %.2f | %.2f | %.2f | %.2f |\n", n, path, float64(values[0])/1e6, s[0], s[1], s[2])
		check(s[2] < 1000, "%d %s exceeds 1s: %.2fms", n, path, s[2])
	}
	// Also include all aged data in an explicit custom dashboard range.
	wide := "/dashboard/data?range=custom&from=" + now.Add(-100*24*time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339)
	data, duration, err := request(server.URL, wide, nil)
	check(err == nil, "all-history dashboard: %v", err)
	var dashboard store.DashboardData
	must(json.Unmarshal(data, &dashboard))
	check(dashboard.TraceCount == int64(n/5), "all-history dashboard missed seeded traces: %d", dashboard.TraceCount)
	fmt.Printf("All-history dashboard (%d traces): %.2fms\n", dashboard.TraceCount, float64(duration)/1e6)
	// Supplemental: this explicit 100-day range is not the default endpoint's
	// <1s budget, but proves that old seeded spans are really included.

	var serial atomic.Int64
	stop := make(chan struct{})
	time.AfterFunc(15*time.Second, func() { close(stop) })
	baseline := traffic(server.URL, paths, stop, &serial)
	checkpoint(dir)
	dbPath := filepath.Join(dir, "spanbox.db")
	before := size(dbPath)
	stop = make(chan struct{})
	trafficDone := make(chan samples, 1)
	go func() { trafficDone <- traffic(server.URL, paths, stop, &serial) }()
	// Wait for every worker to get going, then run the real retention path.
	time.Sleep(100 * time.Millisecond)
	monitorStop := make(chan struct{})
	monitorDone := make(chan struct {
		peak     int64
		segments []string
	}, 1)
	start := time.Now()
	go func() {
		var peak int64
		var segments []string
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		next := time.Time{}
		for {
			peak = max(peak, size(dbPath+"-wal"))
			if time.Now().After(next) {
				var count int
				must(db.Reader().QueryRow("SELECT count(DISTINCT segid) FROM spans_fts_idx").Scan(&count))
				segments = append(segments, fmt.Sprintf("%.2fs:%d", time.Since(start).Seconds(), count))
				next = time.Now().Add(time.Second)
			}
			select {
			case <-monitorStop:
				monitorDone <- struct {
					peak     int64
					segments []string
				}{peak, segments}
				return
			case <-ticker.C:
			}
		}
	}()
	purgeCtx, cancel := context.WithTimeout(context.Background(), purgeTimeout)
	defer cancel()
	deleted, purgeErr := db.Purge(purgeCtx, now.Add(-30*24*time.Hour).UnixNano(), options)
	elapsed := time.Since(start)
	close(stop)
	during := <-trafficDone
	close(monitorStop)
	monitor := <-monitorDone
	peak := monitor.peak
	fmt.Printf("FTS segments (elapsed:count, sampled every 1s throughout Purge): %s\n", strings.Join(monitor.segments, ", "))
	var finalSegments int
	must(db.Reader().QueryRow("SELECT count(DISTINCT segid) FROM spans_fts_idx").Scan(&finalSegments))
	fmt.Printf("FTS segments after purge: %d\n", finalSegments)
	after, wal := size(dbPath), size(dbPath+"-wal")
	check(purgeErr == nil, "purge: %v", purgeErr)
	check(deleted == int64(n/10-5), "deleted %d traces, want %d", deleted, n/10-5)
	for i := 0; i < 5; i++ {
		trace, spans, err := db.GetTrace(context.Background(), id(i))
		check(err == nil && trace.SpanCount == 5 && len(spans) == 5, "protected trace %d lost: %v", i, err)
	}
	remaining := int64(n) - deleted*5 + serial.Load()
	check(count(db, "spans") == remaining, "span count after purge differs from committed ingest/deletes")
	check(count(db, "traces") == int64(n/5)-deleted+serial.Load(), "trace count after purge differs")
	// MATCH consults the actual index, unlike SELECT count(*) FROM external-content FTS.
	var matches int64
	must(db.Reader().QueryRow("SELECT count(*) FROM spans_fts WHERE spans_fts MATCH 'needle'").Scan(&matches))
	check(matches == int64(n)-deleted*5, "FTS stale after purge: %d", matches)
	check(after < before, "DB did not shrink: %d -> %d", before, after)
	check(peak <= 64<<20 && wal <= 8<<20, "WAL unbounded: peak=%d final=%d (limits 64MiB/8MiB)", peak, wal)
	fmt.Println("| Spans | Phase | Kind | Samples | p50 ms | p95 ms | max ms | p95 / baseline | Errors |")
	fmt.Println("|---:|---|---|---:|---:|---:|---:|---:|---:|")
	for _, kind := range []string{"read", "ingest"} {
		b, p := baseline.reads, during.reads
		if kind == "ingest" {
			b, p = baseline.ingest, during.ingest
		}
		bs, ps := stats(b), stats(p)
		fmt.Printf("| %d | baseline | %s | %d | %.2f | %.2f | %.2f | 1.00 | %d |\n", n, kind, len(b), bs[0], bs[1], bs[2], len(baseline.errors))
		fmt.Printf("| %d | purge | %s | %d | %.2f | %.2f | %.2f | %.2f | %d |\n", n, kind, len(p), ps[0], ps[1], ps[2], ps[1]/bs[1], len(during.errors))
		check(len(p) >= 5, "%s traffic did not overlap purge sufficiently", kind)
		check(ps[0] <= 3*bs[0], "%s p50 exceeds 3x baseline: %.2f/%.2fms", kind, ps[0], bs[0])
		// Ingest fairness is p95-based; its MAX is diagnostic, not a threshold.
		check(ps[1] <= 3*bs[1], "%s p95 exceeds 3x baseline: %.2f/%.2fms (purge MAX %.2fms, informational only)", kind, ps[1], bs[1], ps[2])
	}
	stop = make(chan struct{})
	time.AfterFunc(15*time.Second, func() { close(stop) })
	post := traffic(server.URL, paths, stop, &serial)
	fmt.Println("| Spans | Concurrent endpoint | Baseline p95 ms | Purge p95 ms | After p95 ms | Purge max ms | Samples before/during/after |")
	fmt.Println("|---:|---|---:|---:|---:|---:|---|")
	for _, path := range paths {
		b, p, a := stats(baseline.readByPath[path]), stats(during.readByPath[path]), stats(post.readByPath[path])
		fmt.Printf("| %d | %s | %.2f | %.2f | %.2f | %.2f | %d/%d/%d |\n", n, path, b[1], p[1], a[1], p[2], len(baseline.readByPath[path]), len(during.readByPath[path]), len(post.readByPath[path]))
		check(max(b[2], p[2], a[2]) < 1000, "%d %s concurrent page exceeds 1s", n, path)
	}
	searchPath := "/search?q=needle"
	b, p, a := stats(baseline.readByPath[searchPath]), stats(during.readByPath[searchPath]), stats(post.readByPath[searchPath])
	check(len(baseline.readByPath[searchPath]) >= 20 && len(during.readByPath[searchPath]) >= 5 && len(post.readByPath[searchPath]) >= 20, "insufficient per-phase search samples")
	check(p[1] <= 2*b[1], "search p95 exceeds 2x baseline: %.2f/%.2fms", p[1], b[1])
	fmt.Println("Search p95 during purge by 5s window (end seconds:p95 ms:samples):")
	for window := 0; window <= int(elapsed/(5*time.Second)); window++ {
		values := during.searchWindows[window]
		if len(values) > 0 {
			fmt.Printf(" %d:%.2f:%d", (window+1)*5, stats(values)[1], len(values))
		}
	}
	fmt.Println()
	fmt.Println("Ingest p95 during purge by 5s window (end seconds:p95 ms:samples):")
	for window := 0; window <= int(elapsed/(5*time.Second)); window++ {
		values := during.ingestWindows[window]
		if len(values) > 0 {
			fmt.Printf(" %d:%.2f:%d", (window+1)*5, stats(values)[1], len(values))
		}
	}
	fmt.Println()
	for _, phase := range []samples{baseline, during, post} {
		for _, err := range phase.errors {
			check(false, "traffic: %v", err)
		}
	}
	fmt.Printf("| Spans | Purge seconds | Deleted traces | DB before MiB | DB after MiB | WAL peak MiB | WAL final MiB |\n|---:|---:|---:|---:|---:|---:|---:|\n| %d | %.3f | %d | %.2f | %.2f | %.2f | %.2f |\n", n, elapsed.Seconds(), deleted, float64(before)/(1<<20), float64(after)/(1<<20), float64(peak)/(1<<20), float64(wal)/(1<<20))
	return measurement{spans: n, options: options, search: [3]float64{b[1], p[1], a[1]}, ingest: [3]float64{stats(baseline.ingest)[1], stats(during.ingest)[1], stats(during.ingest)[2]}, failures: len(failures) - previousFailures}
}

func main() {
	work := flag.String("work", "", "temporary data directory")
	binary := flag.String("binary", "", "fresh production binary")
	spans := flag.Int("spans", 0, "one dataset size (default: both 50000 and 500000)")
	sweep := flag.Bool("sweep", false, "sweep batches 10/50/200 and -merge-intervals")
	intervals := flag.String("merge-intervals", "1,256", "comma-separated positive explicit merge intervals for -sweep")
	batch := flag.Int("batch-size", config.DefaultPurgeBatchSize, "traces per delete transaction")
	merge := flag.Int("merge-every", config.DefaultFTSMergeEvery, "batches per explicit FTS merge step")
	purgeTimeout := flag.Duration("purge-timeout", 10*time.Minute, "deadline per purge; unfinished candidates fail")
	flag.Parse()
	if *spans < 0 || (*spans > 0 && (*spans < 50 || *spans%10 != 0)) || *batch < 1 || *merge < 1 || *purgeTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "spans must be >=50 and divisible by 10; batch-size, merge-every and purge-timeout must be positive")
		os.Exit(2)
	}
	fmt.Printf("Environment: %s, %s/%s, %d CPUs; localhost HTTP; first + 4 warm samples per page.\n", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	sizes := []int{50000, 500000}
	if *spans != 0 {
		sizes = []int{*spans}
	}
	options := []store.PurgeOptions{{BatchSize: *batch, MergeEvery: *merge}}
	if *sweep {
		options = nil
		for _, batch := range []int{10, 50, 200} {
			for _, interval := range strings.Split(*intervals, ",") {
				merge, err := strconv.Atoi(strings.TrimSpace(interval))
				if err != nil || merge < 1 {
					fmt.Fprintln(os.Stderr, "merge-intervals must contain positive integers")
					os.Exit(2)
				}
				options = append(options, store.PurgeOptions{BatchSize: batch, MergeEvery: merge})
			}
		}
	}
	var results []measurement
	for _, n := range sizes {
		source, now := prepare(*work, *binary, n)
		for _, tuning := range options {
			results = append(results, run(*work, source, now, n, tuning, *purgeTimeout))
		}
		must(os.RemoveAll(source))
	}
	fmt.Println("\nBefore/after search p95 table (production defaults unless -sweep or overrides):")
	fmt.Println("| Spans | Batch | Merge every | Search before ms | Search purge ms | Search after ms | Search ratio | Ingest before p95 ms | Ingest purge p95 ms | Ingest purge MAX ms (info) | Ingest p95 ratio | Failed checks |")
	fmt.Println("|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range results {
		fmt.Printf("| %d | %d | %d | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %d |\n", r.spans, r.options.BatchSize, r.options.MergeEvery, r.search[0], r.search[1], r.search[2], r.search[1]/r.search[0], r.ingest[0], r.ingest[1], r.ingest[2], r.ingest[1]/r.ingest[0], r.failures)
	}
	fmt.Println("Ingest MAX is reported only: writer fairness is whole-purge p95-based, not a hard maximum or a per-window guarantee.")
	if len(failures) > 0 {
		fmt.Printf("FAIL: %d assertions\n", len(failures))
		os.Exit(1)
	}
	fmt.Println("PASS: retention correctness, disabled retention, concurrent HTTP reads/OTLP ingest, shrinkage, bounded WAL, all pages <1s, search p95 within 2x baseline and ingest p95 within 3x baseline")
}
