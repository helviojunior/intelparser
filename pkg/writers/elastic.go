package writers

import (
	"encoding/json"
	"time"
	"net/url"
	"math"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"context"
	"errors"
	"bytes"
	"net/http"
	"crypto/tls"
	//"reflect"
	"io"
	"os"
    "strconv"

	"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/models"
	elk "github.com/elastic/go-elasticsearch/v8"
	esapi "github.com/elastic/go-elasticsearch/v8/esapi"
	logger "github.com/helviojunior/intelparser/pkg/log"
)

// fields in the main model to ignore
var elkExludedFields = []string{"failed", "failed_reason", "near_text"}
var elkBulkCount = 200
var elkBulkMaxSize = 5 * 1024 * 1024
var elkWorkers = 4
var elkQueueSize = 1024
var elkRefreshInterval = "30s"
var elkTranslogDurability = "async"
var elkReplicas = -1 // -1 means "do not change"

// queueItem wraps a File with the timestamp it was enqueued at, so workers
// can measure queue-wait time (producer-to-consumer latency).
type queueItem struct {
	file       *models.File
	enqueuedAt time.Time
}

// JsonWriter is a JSON lines writer
type ElasticWriter struct {
	Client *elk.Client
	Index string

	// debug toggles the log level of operational (per-bulk, per-file, periodic
	// metrics) messages. When true they are emitted at Info; when false they
	// are emitted at Debug and are invisible unless global --debug is set.
	debug bool

	queue    chan *queueItem
	wg       sync.WaitGroup
	closed   atomic.Bool
	failures atomic.Int64

	// Metrics (monotonically increasing, read atomically).
	metBulks       atomic.Int64
	metBulkRetries atomic.Int64
	metDocs        atomic.Int64
	metBytes       atomic.Int64
	metLatencyNs   atomic.Int64 // sum of per-request bulk durations
	metLatencyMax  atomic.Int64 // max observed bulk duration
	metFiles       atomic.Int64
	metFileTimeNs  atomic.Int64 // sum of writeSync durations
	metQueueWaitNs atomic.Int64 // sum of queue-wait durations

	startedAt    time.Time
	stopReporter chan struct{}
	reporterWG   sync.WaitGroup
}

type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index struct {
			ID     string `json:"_id"`
			Result string `json:"result"`
			Status int    `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
				Cause  struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"caused_by"`
			} `json:"error"`
		} `json:"index"`
	} `json:"items"`
}

type indexResponse struct {
	ID     string `json:"_id"`
	Index  string `json:"_index"`
	Result string `json:"result"`
	Error  struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
		Cause  struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"caused_by"`
	} `json:"error"`
}

type Interceptor struct {
  base   	*http.Transport
}


func (i Interceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	// Header exigido pelo client do ES
	const prodHeaderKey = "X-Elastic-Product"
	const prodHeaderVal = "Elasticsearch"

	// O client do ES costuma checar GET /
	if (req.Method == http.MethodGet || req.Method == http.MethodHead) && req.URL.Path == "/" {
		str_body := ""
		if req.Method != http.MethodHead {
		
			str_body = `{
			  "version": { "number": "8.0.0-SNAPSHOT", "build_flavor": "default" },
			  "tagline": "You Know, for Search"
			}`
		}

		resp := &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(str_body)),
			Header:     make(http.Header),
			Request:    req,
		}
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set(prodHeaderKey, prodHeaderVal)
		return resp, nil
	}

	resp, err := i.base.RoundTrip(req)
	if resp != nil {
		resp.Header.Set(prodHeaderKey, prodHeaderVal)
	}
	return resp, err
}

// NewElasticWriter returns a new Elasticsearch writer.
// When debug is true, operational logs are emitted at Info level; otherwise
// they are emitted at Debug level.
func NewElasticWriter(uri string, debug bool) (*ElasticWriter, error) {

	u, err := url.Parse(uri)
	if err != nil {
	    return nil, err
	}

	username := u.User.Username()
	password, _ := u.User.Password()
	port := u.Port()
	if port == "" {
		port = "9200"
	}
	index_name := u.EscapedPath()
	index_name = strings.Trim(index_name, "/ ")
	index_name = strings.SplitN(index_name, "/", 2)[0]
	if index_name == "" {
		index_name = "intelparser"
	}

	wr := &ElasticWriter{
		Index: index_name,
		debug: debug,
	}

	conf := elk.Config{
	    Addresses: []string{
            fmt.Sprintf("%s://%s:%s/", u.Scheme, u.Hostname(), port),
        },
        //Username: username,
        //Password: password,
        //CACert:   cert,
		RetryOnStatus: []int{429, 502, 503, 504},
		MaxRetries:    5,
		RetryBackoff:  func(i int) time.Duration {
			// A simple exponential delay
			d := time.Duration(math.Exp2(float64(i))) * time.Second
			if debug {
				logger.Infof("Elastic retry, attempt: %d | Sleeping for %s...", i, d)
			} else {
				logger.Debugf("Elastic retry, attempt: %d | Sleeping for %s...", i, d)
			}
			return d
		},
		CompressRequestBody: true,
		Transport: &Interceptor{
			&http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 64,
				MaxConnsPerHost:     64,
			    IdleConnTimeout:     90 * time.Second,
			    DisableCompression:  false,
			    ForceAttemptHTTP2:   true,
			    TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	// Check username and password from Environment Variables
	if v1, ok := os.LookupEnv("INTELPARSER_OUTPUT_USERNAME"); ok {
		conf.Username = v1;
		logger.Infof("Setting username %s using env.INTELPARSER_OUTPUT_USERNAME", v1)
	}
	if v1, ok := os.LookupEnv("INTELPARSER_OUTPUT_PASSWORD"); ok {
		conf.Password = v1;
		logger.Infof("Setting password using env.INTELPARSER_OUTPUT_PASSWORD")
	}

	if username != "" && password != "" {
		conf.Username = username
		conf.Password = password
	}

	wr.Client, err = elk.NewClient(conf)
	if err != nil {
	    return nil, err
	}

	// Faz um ping (chama GET / internamente)
	res, err := wr.Client.Ping()
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if v1, ok := os.LookupEnv("ELK_BULK_SIZE"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 > 10 {
				logger.Infof("Setting maximum ELK bulk count as %d using env.ELK_BULK_SIZE", i1)
				elkBulkCount = int(i1)
			}  
		}
	}

	if v1, ok := os.LookupEnv("ELK_BULK_BYTES"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 > 4094 {
				logger.Infof("Setting maximum ELK bulk size as %s using env.ELK_BULK_BYTES", tools.Bytes(uint64(i1)))
				elkBulkMaxSize = int(i1)
			}
		}
	}

	if v1, ok := os.LookupEnv("ELK_WORKERS"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 >= 1 && i1 <= 64 {
				logger.Infof("Setting ELK writer workers to %d using env.ELK_WORKERS", i1)
				elkWorkers = int(i1)
			}
		}
	}

	if v1, ok := os.LookupEnv("ELK_QUEUE_SIZE"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 >= 1 && i1 <= 100000 {
				logger.Infof("Setting ELK writer queue size to %d using env.ELK_QUEUE_SIZE", i1)
				elkQueueSize = int(i1)
			}
		}
	}

	if v1, ok := os.LookupEnv("ELK_REFRESH_INTERVAL"); ok {
		logger.Infof("Setting ELK refresh_interval to %s using env.ELK_REFRESH_INTERVAL", v1)
		elkRefreshInterval = v1
	}

	if v1, ok := os.LookupEnv("ELK_TRANSLOG_DURABILITY"); ok {
		logger.Infof("Setting ELK translog.durability to %s using env.ELK_TRANSLOG_DURABILITY", v1)
		elkTranslogDurability = v1
	}

	if v1, ok := os.LookupEnv("ELK_REPLICAS"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 >= 0 && i1 <= 10 {
				logger.Infof("Setting ELK number_of_replicas to %d using env.ELK_REPLICAS", i1)
				elkReplicas = int(i1)
			}
		}
	}

	//File Index
	err = wr.CreateIndex(wr.Index, `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "indexed_at": {"type": "date"},
                    "leak_date": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "name": {"type": "keyword"},
                    "file_name": {"type": "text"},
                    "file_path": {"type": "keyword"},
                    "mime_type": {"type": "keyword"},
                    "size": {"type": "long"},
                    "provider": {"type": "keyword"},
                    "provider_id": {"type": "text"},
                    "bucket": {"type": "text"},
                    "media_type": {"type": "text"},
                    "content": {"type": "text"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	//Credential Index
	err = wr.CreateIndex(wr.Index + "_creds", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "rule": {"type": "keyword"},
                    "user_domain": {"type": "keyword"},
                    "username": {"type": "keyword"},
                    "password": {"type": "keyword"},
                    "cpf": {"type": "keyword"},
                    "url": {"type": "keyword"},
                    "url_domain": {"type": "keyword"},
                    "severity": {"type": "long"},
                    "entropy": {"type": "long"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	//Urls Index
	err = wr.CreateIndex(wr.Index + "_urls", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "domain": {"type": "keyword"},
                    "url": {"type": "keyword"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}


	//Emails Index
	err = wr.CreateIndex(wr.Index + "_emails", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "domain": {"type": "keyword"},
                    "email": {"type": "keyword"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	// Apply ingest-friendly settings to all managed indices (new and existing).
	for _, idx := range []string{wr.Index, wr.Index + "_creds", wr.Index + "_urls", wr.Index + "_emails"} {
		if err := wr.applyIngestSettings(idx); err != nil {
			logger.Warnf("Could not apply ingest settings to %s: %s", idx, err)
		}
	}

	// Start async worker pool and metrics reporter.
	wr.queue = make(chan *queueItem, elkQueueSize)
	wr.stopReporter = make(chan struct{})
	wr.startedAt = time.Now()
	logger.Infof("Starting ELK writer with %d workers (queue=%d, bulk=%d docs/%s)",
		elkWorkers, elkQueueSize, elkBulkCount, tools.Bytes(uint64(elkBulkMaxSize)))
	for i := 0; i < elkWorkers; i++ {
		wr.wg.Add(1)
		go wr.worker()
	}
	wr.reporterWG.Add(1)
	go wr.metricsReporter()

	return wr, nil
}

// applyIngestSettings tunes an index for bulk ingestion throughput.
// Applied once on writer init so it also updates existing indices.
func (ew *ElasticWriter) applyIngestSettings(index string) error {
	body := map[string]interface{}{
		"index": map[string]interface{}{
			"refresh_interval": elkRefreshInterval,
			"translog": map[string]interface{}{
				"durability": elkTranslogDurability,
			},
		},
	}
	if elkReplicas >= 0 {
		body["index"].(map[string]interface{})["number_of_replicas"] = elkReplicas
	}

	b, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req := esapi.IndicesPutSettingsRequest{
		Index: []string{index},
		Body:  bytes.NewReader(b),
	}
	res, err := req.Do(context.Background(), ew.Client)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("status %d: %s", res.StatusCode, res.String())
	}
	ew.logf("Applied ingest settings to %s: %s", index, string(b))
	return nil
}

// logf emits an operational log message at Info when the writer was created
// with debug=true, or at Debug otherwise. Use for per-bulk / per-file /
// periodic metrics logs that would otherwise be too noisy in Info.
func (ew *ElasticWriter) logf(format string, args ...interface{}) {
	if ew.debug {
		logger.Infof(format, args...)
	} else {
		logger.Debugf(format, args...)
	}
}

// Write enqueues the result for asynchronous ingestion by the worker pool.
// Errors on the async path are logged and counted; they are not returned here.
func (ew *ElasticWriter) Write(result *models.File) error {
	if ew.closed.Load() {
		return errors.New("ElasticWriter is closed")
	}
	// Shallow-copy the File so writeSync can null out the heavy slices on its
	// local copy without racing with other writers that share the same pointer.
	cp := *result
	ew.queue <- &queueItem{file: &cp, enqueuedAt: time.Now()}
	return nil
}

// worker consumes files from the queue and writes them synchronously.
func (ew *ElasticWriter) worker() {
	defer ew.wg.Done()
	for item := range ew.queue {
		wait := time.Since(item.enqueuedAt)
		ew.metQueueWaitNs.Add(int64(wait))

		start := time.Now()
		err := ew.writeSync(item.file)
		dur := time.Since(start)

		ew.metFiles.Add(1)
		ew.metFileTimeNs.Add(int64(dur))

		if err != nil {
			ew.failures.Add(1)
			logger.Errorf("Elastic writer failure for %s: %s", item.file.FileName, err)
			continue
		}

		ew.logf("ELK file done: %s queue_wait=%s write=%s q=%d/%d",
			item.file.FileName, wait, dur, len(ew.queue), cap(ew.queue))
	}
}

// Flush closes the queue and waits for all in-flight writes to complete.
// Must be called once, after producers have stopped invoking Write.
func (ew *ElasticWriter) Flush() error {
	if !ew.closed.CompareAndSwap(false, true) {
		return nil
	}
	qlen := len(ew.queue)
	if qlen > 0 {
		logger.Infof("Flushing ELK writer: %d file(s) pending in queue", qlen)
	}
	close(ew.queue)
	ew.wg.Wait()

	// Stop the metrics reporter and emit a final summary.
	close(ew.stopReporter)
	ew.reporterWG.Wait()
	ew.logMetrics(true)

	if n := ew.failures.Load(); n > 0 {
		logger.Warnf("ELK writer finished with %d failure(s)", n)
	}
	return nil
}

// recordBulk atomically updates bulk-level metrics. Called by CreateDocBulk on
// successful completion. dur is the duration of the successful HTTP request.
func (ew *ElasticWriter) recordBulk(docs int, size int, dur time.Duration) {
	ew.metBulks.Add(1)
	ew.metDocs.Add(int64(docs))
	ew.metBytes.Add(int64(size))
	ew.metLatencyNs.Add(int64(dur))
	// max latency via CAS loop
	d := int64(dur)
	for {
		cur := ew.metLatencyMax.Load()
		if d <= cur || ew.metLatencyMax.CompareAndSwap(cur, d) {
			break
		}
	}
}

// metricsReporter emits a periodic summary of writer throughput / latency so
// operators can spot whether the bottleneck is on the parser side (queue near
// empty), on the writer side (queue full, high avg_bulk), or elsewhere.
func (ew *ElasticWriter) metricsReporter() {
	defer ew.reporterWG.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ew.stopReporter:
			return
		case <-ticker.C:
			ew.logMetrics(false)
		}
	}
}

// logMetrics prints a one-line snapshot of accumulated counters. When final
// is true the tag changes so the caller can spot the end-of-run summary in
// logs.
func (ew *ElasticWriter) logMetrics(final bool) {
	bulks := ew.metBulks.Load()
	docs := ew.metDocs.Load()
	bytes := ew.metBytes.Load()
	latSum := ew.metLatencyNs.Load()
	latMax := ew.metLatencyMax.Load()
	files := ew.metFiles.Load()
	ftime := ew.metFileTimeNs.Load()
	qwait := ew.metQueueWaitNs.Load()
	errs := ew.failures.Load()
	retries := ew.metBulkRetries.Load()

	elapsed := time.Since(ew.startedAt).Seconds()
	if elapsed < 0.001 {
		elapsed = 0.001
	}

	var avgBulk, avgFile, avgQWait time.Duration
	if bulks > 0 {
		avgBulk = time.Duration(latSum / bulks)
	}
	if files > 0 {
		avgFile = time.Duration(ftime / files)
		avgQWait = time.Duration(qwait / files)
	}

	qlen, qcap := 0, 0
	if ew.queue != nil {
		qlen = len(ew.queue)
		qcap = cap(ew.queue)
	}

	// The final summary is always emitted at Info level so end-of-run stats
	// are visible regardless of the debug flag. Periodic snapshots follow the
	// flag.
	tag := "ELK metrics"
	emit := ew.logf
	if final {
		tag = "ELK final metrics"
		emit = logger.Infof
	}

	emit("%s: queue=%d/%d files=%d (%.1f/s) bulks=%d docs=%d (%.0f/s) bytes=%s (%s/s) avg_bulk=%s max_bulk=%s avg_file=%s avg_queue_wait=%s retries=%d errs=%d",
		tag,
		qlen, qcap,
		files, float64(files)/elapsed,
		bulks,
		docs, float64(docs)/elapsed,
		tools.Bytes(uint64(bytes)), tools.Bytes(uint64(float64(bytes)/elapsed)),
		avgBulk, time.Duration(latMax),
		avgFile, avgQWait,
		retries, errs,
	)
}

// Finalize renders a human-friendly end-of-run table with the writer's
// aggregated ingestion statistics. Safe to call after Flush.
func (ew *ElasticWriter) Finalize() error {
	bulks := ew.metBulks.Load()
	retries := ew.metBulkRetries.Load()
	docs := ew.metDocs.Load()
	sizeBytes := ew.metBytes.Load()
	latSum := ew.metLatencyNs.Load()
	latMax := ew.metLatencyMax.Load()
	files := ew.metFiles.Load()
	ftime := ew.metFileTimeNs.Load()
	qwait := ew.metQueueWaitNs.Load()
	errs := ew.failures.Load()

	elapsed := time.Since(ew.startedAt)
	elapsedSecs := elapsed.Seconds()
	if elapsedSecs < 0.001 {
		elapsedSecs = 0.001
	}

	var avgBulk, avgFile, avgQWait time.Duration
	if bulks > 0 {
		avgBulk = time.Duration(latSum / bulks)
	}
	if files > 0 {
		avgFile = time.Duration(ftime / files)
		avgQWait = time.Duration(qwait / files)
	}

	docsPerSec := float64(docs) / elapsedSecs
	filesPerSec := float64(files) / elapsedSecs
	bytesPerSec := float64(sizeBytes) / elapsedSecs

	rows := [][2]string{
		{"Elapsed time", elapsed.Truncate(time.Second).String()},
		{"Files processed", fmt.Sprintf("%s (%.1f/s)",
			fmtInt(files), filesPerSec)},
		{"Bulks sent", fmt.Sprintf("%s (%s retries)",
			fmtInt(bulks), fmtInt(retries))},
		{"Documents indexed", fmt.Sprintf("%s (%s/s)",
			fmtInt(docs), fmtRate(docsPerSec))},
		{"Data sent", fmt.Sprintf("%s (%s/s)",
			tools.Bytes(uint64(sizeBytes)), tools.Bytes(uint64(bytesPerSec)))},
		{"Bulk latency", fmt.Sprintf("avg %s, max %s",
			avgBulk.Truncate(time.Millisecond), time.Duration(latMax).Truncate(time.Millisecond))},
		{"File write time (avg)", avgFile.Truncate(time.Millisecond).String()},
		{"Queue wait (avg)", avgQWait.Truncate(time.Microsecond).String()},
		{"Failures", fmtInt(errs)},
	}

	table := renderKVTable("ELK ingestion summary", rows)
	// Print raw to stdout so the box-drawing characters are not mangled by
	// a structured logger.
	fmt.Print(table)
	return nil
}

// fmtInt formats an integer with thousands separators.
func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return "-" + fmtInt(-n)
	}
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

// fmtRate formats a rate with k/M suffix for readability.
func fmtRate(r float64) string {
	switch {
	case r >= 1_000_000:
		return fmt.Sprintf("%.1fM", r/1_000_000)
	case r >= 1_000:
		return fmt.Sprintf("%.1fk", r/1_000)
	default:
		return fmt.Sprintf("%.0f", r)
	}
}

// renderKVTable builds a two-column box-drawing table with a "Métrica/Valor"
// header, a row separator between every data row (to match the style shown
// in the spec), and column widths auto-fit to the longest value.
func renderKVTable(title string, rows [][2]string) string {
	const keyHeader = "Métrica"
	const valHeader = "Valor"

	// runeLen returns the visual width in runes (assumes monospace with
	// 1 cell per rune, good enough for ASCII + common latin extended).
	runeLen := func(s string) int { return len([]rune(s)) }

	maxK, maxV := runeLen(keyHeader), runeLen(valHeader)
	for _, r := range rows {
		if n := runeLen(r[0]); n > maxK {
			maxK = n
		}
		if n := runeLen(r[1]); n > maxV {
			maxV = n
		}
	}
	// 1-space padding on each side of the cell.
	kw := maxK + 2
	vw := maxV + 2

	top    := "┌" + strings.Repeat("─", kw) + "┬" + strings.Repeat("─", vw) + "┐"
	sep    := "├" + strings.Repeat("─", kw) + "┼" + strings.Repeat("─", vw) + "┤"
	bottom := "└" + strings.Repeat("─", kw) + "┴" + strings.Repeat("─", vw) + "┘"

	center := func(s string, width int) string {
		extra := width - runeLen(s)
		if extra <= 0 {
			return s
		}
		left := extra / 2
		right := extra - left
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
	}
	left := func(s string, width int) string {
		pad := width - runeLen(s) - 2
		if pad < 0 {
			pad = 0
		}
		return " " + s + strings.Repeat(" ", pad) + " "
	}

	var b strings.Builder
	if title != "" {
		b.WriteString(title + "\n")
	}
	b.WriteString(top + "\n")
	b.WriteString("│" + center(keyHeader, kw) + "│" + center(valHeader, vw) + "│\n")
	b.WriteString(sep + "\n")
	// Data rows are emitted back-to-back without a separator between them —
	// keeps the table compact, especially when the surrounding pipeline
	// prefixes every line with a timestamp.
	for _, r := range rows {
		b.WriteString("│" + left(r[0], kw) + "│" + left(r[1], vw) + "│\n")
	}
	b.WriteString(bottom + "\n")
	return b.String()
}

// hashable is satisfied by Credential, URL and Email (see models.go). Used
// by ingestItems to compute each doc's deterministic _id.
type hashable interface {
	CalcHash(string) string
}

// ingestItems marshals each item, attaches the standard envelope fields, and
// ships the resulting docs to `index` in bulks respecting elkBulkCount and
// elkBulkMaxSize. Kept generic so the per-index loops in writeSync stay
// identical and can run concurrently.
func ingestItems[T hashable](ew *ElasticWriter, index string, items []T,
	fingerprint, bucket string) error {

	docs := make(map[string][]byte)
	docsLen := 0

	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			return err
		}

		cid := it.CalcHash(fingerprint)
		b, err = ew.MarshalAppend(b, map[string]interface{}{
			"file_id":     fingerprint,
			"bucket":      bucket,
			"fingerprint": cid,
		})
		if err != nil {
			return err
		}

		docs[cid] = b
		docsLen += len(b)

		if len(docs) >= elkBulkCount || docsLen >= elkBulkMaxSize {
			if err := ew.CreateDocBulk(index, docs); err != nil {
				return err
			}
			docs = make(map[string][]byte)
			docsLen = 0
		}
	}

	if len(docs) > 0 {
		if err := ew.CreateDocBulk(index, docs); err != nil {
			return err
		}
	}
	return nil
}

// writeSync performs the actual bulk HTTP calls against OpenSearch.
// The three per-type ingestions (creds / urls / emails) run concurrently so
// big files don't serialize them behind each other; the single-doc file
// write is done after all three complete.
func (ew *ElasticWriter) writeSync(result *models.File) error {
	ew.logf("Integrating elastic (file=%s): %d credentials, %d e-mails, %d urls",
		result.FileName, len(result.Credentials), len(result.Emails), len(result.URLs))

	var wg sync.WaitGroup
	errs := make([]error, 3)

	wg.Add(3)
	go func() {
		defer wg.Done()
		errs[0] = ingestItems(ew, ew.Index+"_creds",
			result.Credentials, result.Fingerprint, result.Bucket)
	}()
	go func() {
		defer wg.Done()
		errs[1] = ingestItems(ew, ew.Index+"_urls",
			result.URLs, result.Fingerprint, result.Bucket)
	}()
	go func() {
		defer wg.Done()
		errs[2] = ingestItems(ew, ew.Index+"_emails",
			result.Emails, result.Fingerprint, result.Bucket)
	}()
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return e
		}
	}

	// File doc — build a local copy without the heavy slices so the caller's
	// File (and any other writers sharing the pointer) are not mutated.
	fileDoc := *result
	fileDoc.Credentials = nil
	fileDoc.Emails = nil
	fileDoc.URLs = nil

	b_data, err := json.Marshal(fileDoc)
	if err != nil {
		return err
	}

	res, err := ew.Client.Index(ew.Index, bytes.NewReader(b_data),
		ew.Client.Index.WithDocumentID(result.Fingerprint))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return fmt.Errorf("Cannot create/update file document: %s", res.String())
	}

	return nil
}

func (ew *ElasticWriter) CreateIndex(index string, mapping string) error {

	var raw map[string]interface{}

	response, err := ew.Client.Indices.Exists([]string{index})
	if err != nil {
	    return err
	}
	defer response.Body.Close()

    if response.IsError() {

		if response.StatusCode == 404 {
			indexReq := esapi.IndicesCreateRequest{
			    Index: index,
			    Body: strings.NewReader(string(mapping)),
			}

			logger.Infof("Creating elastic index %s", index)
			res, err := indexReq.Do(context.Background(), ew.Client)
			if err != nil {
			    return err
			}
			defer res.Body.Close()

			if res.IsError() {

		        if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		            return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
		        } else {
		            return errors.New(fmt.Sprintf("Cannot create/update elastic index [%d] %s: %s",
		                res.StatusCode,
		                raw["error"].(map[string]interface{})["type"],
		                raw["error"].(map[string]interface{})["reason"],
		            ))
		        }

			}

		}else{

	        if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
	            return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
	        } else {
	            return errors.New(fmt.Sprintf("Cannot get elastic index [%d] %s: %s",
	                response.StatusCode,
	                raw["error"].(map[string]interface{})["type"],
	                raw["error"].(map[string]interface{})["reason"],
	            ))
	        }


		}

    }

    return nil

}

func (ew *ElasticWriter) CreateDocBulk(index string, docs map[string][]byte) error {
    var raw map[string]interface{}
    var buf bytes.Buffer
    size := 0
    for id, doc := range docs {
    	meta := []byte(fmt.Sprintf(`{ "index" : { "_id" : "%s" } }%s`, id, "\n"))
    	data := []byte(doc)
    	data = append(data, "\n"...)

    	size += len(meta) + len(data)
    	buf.Grow(len(meta) + len(data))
		buf.Write(meta)
		buf.Write(data)

    }

    ew.logf("Elastic bulk start: %d docs, %s -> %s", len(docs), tools.Bytes(uint64(size)), index)

    start := time.Now()
    for i := range 10 {

        reqStart := time.Now()
        res, err := ew.Client.Bulk(bytes.NewReader(buf.Bytes()), ew.Client.Bulk.WithIndex(index))
        if err != nil {
            return err
        }
        defer res.Body.Close()
        reqDur := time.Since(reqStart)
        if i > 0 {
            ew.metBulkRetries.Add(1)
        }

        if res.IsError() {

            if i >= 5 {
                if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
                    return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
                } else {
                    return errors.New(fmt.Sprintf("Error: [%d] %s: %s",
                        res.StatusCode,
                        raw["error"].(map[string]interface{})["type"],
                        raw["error"].(map[string]interface{})["reason"],
                    ))
                }

            }

            // A successful response might still contain errors for particular documents...
            //
        } else {
        	var blk *bulkResponse
            if err := json.NewDecoder(res.Body).Decode(&blk); err != nil {
                return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
            } else {
                // Count item-level errors and log a single aggregated line
                // instead of spamming one log per failed doc.
                itemErrs := 0
                var firstErr string
                for _, d := range blk.Items {
                    if d.Index.Status > 201 {
                        itemErrs++
                        if firstErr == "" {
                            firstErr = fmt.Sprintf("[%d] %s: %s",
                                d.Index.Status, d.Index.Error.Type, d.Index.Error.Reason)
                        }
                    }
                }
                if itemErrs > 0 {
                    ew.logf("Elastic bulk %s: %d/%d items failed (first: %s)",
                        index, itemErrs, len(blk.Items), firstErr)
                }
            }
        }

        if res.StatusCode == 200 || res.StatusCode == 201 {
            total := time.Since(start)
            bps := float64(size) / total.Seconds()
            dps := float64(len(docs)) / total.Seconds()
            ew.recordBulk(len(docs), size, reqDur)
            ew.logf("Elastic bulk OK %s: %d docs, %s in %s (req=%s, %.0f docs/s, %s/s)",
                index, len(docs), tools.Bytes(uint64(size)), total, reqDur,
                dps, tools.Bytes(uint64(bps)))
            return nil
        }

        ew.logf("Elastic bulk attempt %d on %s failed with status %d in %s; retrying",
            i+1, index, res.StatusCode, reqDur)
        time.Sleep(1 * time.Second)
    }

    return errors.New("Cannot create/update document")
}


func (ew *ElasticWriter) CreateDoc(index string, data []byte, doc_id string) error {
	var raw map[string]interface{}
	for i := range 10 {
		res, err := ew.Client.Index(index, bytes.NewReader(data), ew.Client.Index.WithDocumentID(doc_id))
		if err != nil {
		    return err
		}
		defer res.Body.Close()

		if res.IsError() {

			if i >= 5 {
				if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
					return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
				} else {
					return errors.New(fmt.Sprintf("Error: [%d] %s: %s",
						res.StatusCode,
						raw["error"].(map[string]interface{})["type"],
						raw["error"].(map[string]interface{})["reason"],
					))
				}

			}

			// A successful response might still contain errors for particular documents...
			//
		} else {

			if res.StatusCode == 200 || res.StatusCode == 201 {
				return nil
			}

			//bodyBytes, err := io.ReadAll(res.Body)
		    //if err != nil {
		    //    return err
		    //}
		    //bodyString := string(bodyBytes)
			//fmt.Printf("Resp: %s", bodyString)

			var idxRes *indexResponse
			
			if err := json.NewDecoder(res.Body).Decode(&idxRes); err != nil {
				return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
			} else {
				//Debug result
			}
		}

		time.Sleep(1 * time.Second)
	}

	return errors.New("Cannot create/update document")
}



func (ew *ElasticWriter) MarshalAppend(marshalled []byte, new_data map[string]interface{}) ([]byte, error) {
	t_data := make(map[string]interface{})
	err := json.Unmarshal(marshalled, &t_data)

	data := make(map[string]interface{})
	for k, v := range t_data {
		// skip excluded fields
		if tools.SliceHasStr(elkExludedFields, k) {
			continue
		}

		data[k] = v
    }

    for k, v := range new_data {
    	data[k] = v
    }

	j_data, err := json.Marshal(data)
	if err != nil {
		return []byte{}, err
	}

	return j_data, nil
}


func (ew *ElasticWriter) Marshal(v any) ([]byte, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return []byte{}, err
	}

	t_data := make(map[string]interface{})
	err = json.Unmarshal(j, &t_data)

	data := make(map[string]interface{})
	for k, v := range t_data {
		// skip excluded fields
		if tools.SliceHasStr(elkExludedFields, k) {
			continue
		}

		data[k] = v
    }

	j_data, err := json.Marshal(data)
	if err != nil {
		return []byte{}, err
	}

	return j_data[:], nil
}
