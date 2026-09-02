package writers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	//"reflect"
	"io"
	"os"
	"strconv"

	elk "github.com/elastic/go-elasticsearch/v8"
	esapi "github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/helviojunior/intelparser/internal/tools"
	logger "github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/models"
)

// fields in the main model to ignore. "fingerprint" is stripped from the file
// document because it is used as the document _id — storing it again as a field
// would be redundant (same for the leak indices, whose _id is the content hash).
var elkExludedFields = []string{"failed", "failed_reason", "near_text", "fingerprint"}
var elkBulkCount = 200
var elkBulkMaxSize = 5 * 1024 * 1024
var elkWorkers = 4
var elkQueueSize = 1024
var elkRefreshInterval = "30s"
var elkTranslogDurability = "async"

// elkMetricsInterval is how often the periodic "ELK metrics" snapshot is
// emitted (ELK_METRICS_INTERVAL, in seconds; 0 disables it). The snapshot is
// logged at Info rather than Debug: it is a single line per interval, and it is
// the only thing that says, during a multi-hour import, whether the run is
// bound by the source (queue empty), by the writer (queue full) or by the
// cluster itself (docs/s flat while the queue stays full). Reconstructing that
// from the outside costs far more than the line costs.
var elkMetricsInterval = 15 * time.Second

// elkPauseInterval is how long the writer waits before re-probing a cluster
// that is refusing writes (ELK_PAUSE_INTERVAL, in seconds). The condition it
// is built for -- a flood-stage disk watermark flipping an index to
// read-only-allow-delete -- clears when an operator frees space or the cluster
// finishes a merge, which is a human-scale wait, not a millisecond one.
var elkPauseInterval = 30 * time.Second

// elkReplicas is the replica count new indices are CREATED with (ELK_REPLICAS).
// Default 0: a replica shard is never allocated on the same node as its
// primary, so on a single-node cluster it would sit UNASSIGNED forever and pin
// health at yellow. Set ELK_REPLICAS=1 (or higher) on a multi-node cluster.
//
// It deliberately does not reach indices that already exist — see
// elkReplicasUpdate.
var elkReplicas = 0

// elkReplicasUpdate is what applyIngestSettings writes to number_of_replicas on
// an index that already exists. -1 means "do not change", and nothing sets it:
// the replica count of a live index is an operational decision that may have
// been made outside this tool, and silently resetting it on every import would
// be a destructive side effect of a routine run. ELK_REPLICAS is a
// creation-time choice only.
var elkReplicasUpdate = -1

// elkCodec is the Lucene store codec applied at index-creation time. It is a
// static setting (settable only on creation, never on an open index), so it
// must live in the create body — unlike refresh/translog/replicas which are
// patched live by applyIngestSettings. "best_compression" (DEFLATE) trades a
// little CPU on merge for ~20-25% less disk than the default "default" (LZ4),
// which matters a lot for these keyword/text-heavy leak indices.
var elkCodec = "best_compression"

// Per-index primary shard counts. Like index.codec, number_of_shards is a
// static setting — settable only at creation, never patched on an open index —
// so getting it wrong means a _split, _shrink or full _reindex later. Each
// family gets its own knob because they differ by orders of magnitude: _ctrl
// holds one document per file while _creds can reach tens of GB. Default 1;
// raise a family only once its shards approach the 10-50GB band.
var elkCtrlShards = 1
var elkCredsShards = 1
var elkUrlsShards = 1
var elkEmailsShards = 1
var elkPhoneShards = 1
var elkDocsShards = 1
var elkRefsShards = 1

// elkLeakDateScript keeps a deduplicated leak's two dates correct no matter
// what order the updates that touch it arrive in: inserted_at converges on the
// EARLIEST timestamp ever seen for that leak and last_reference_at on the
// LATEST one. Both are stored as RFC3339 UTC strings, which are fixed-width and
// zero-suffixed, so lexicographic order is chronological order and a plain
// String.compareTo is enough.
//
// Doing this server-side is what allows more than one writer worker: the older
// formulation ("doc" always overwriting last_reference_at, "upsert" seeding
// inserted_at only on first insertion) made both dates depend on write order,
// so last_reference_at ended up being whichever file happened to be written
// last rather than the most recent one, and a re-import of an older file would
// rewind it.
const elkLeakDateScript = `if (ctx._source.inserted_at == null || ctx._source.inserted_at.compareTo(params.first) > 0) { ctx._source.inserted_at = params.first } if (ctx._source.last_reference_at == null || ctx._source.last_reference_at.compareTo(params.last) < 0) { ctx._source.last_reference_at = params.last }`

// elkLeakScriptJSON is elkLeakDateScript pre-quoted as a JSON string literal,
// built once because it is embedded in every single leak update line.
var elkLeakScriptJSON = jsonQuote(elkLeakDateScript)

// pendingLeak is one deduplicated leak waiting in a bulk buffer. doc holds the
// intrinsic fields already marshalled (dates excluded, since they are spliced
// in at flush time); first/last are the running min/max of the timestamps of
// every occurrence coalesced into this entry.
type pendingLeak struct {
	doc   []byte
	first string
	last  string
}

// clusterGate holds every writer goroutine still while Elasticsearch is
// refusing writes. One goroutine owns the pause and re-probes on an interval;
// the rest park until it succeeds, so a cluster that is already struggling is
// not hammered by sixteen workers rediscovering the same block.
//
// The zero value is an open gate.
type clusterGate struct {
	mu      sync.Mutex
	resumed chan struct{} // non-nil while paused, closed on resume
	since   time.Time
	total   atomic.Int64 // ns spent paused across the run, for the metrics line
}

// wait blocks until the cluster is accepting writes again.
func (g *clusterGate) wait() {
	g.mu.Lock()
	ch := g.resumed
	g.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// pause engages the gate and reports whether this caller is the one that
// engaged it. Only that caller should probe.
func (g *clusterGate) pause() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resumed != nil {
		return false
	}
	g.resumed = make(chan struct{})
	g.since = time.Now()
	return true
}

// resume releases everyone waiting and returns how long the pause lasted.
func (g *clusterGate) resume() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resumed == nil {
		return 0
	}
	close(g.resumed)
	g.resumed = nil
	d := time.Since(g.since)
	g.total.Add(int64(d))
	return d
}

// pausedFor reports how long the current pause has been running, or 0 when the
// gate is open.
func (g *clusterGate) pausedFor() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resumed == nil {
		return 0
	}
	return time.Since(g.since)
}

// queueItem wraps a File with the timestamp it was enqueued at, so workers
// can measure queue-wait time (producer-to-consumer latency).
type queueItem struct {
	file       *models.File
	enqueuedAt time.Time
}

// JsonWriter is a JSON lines writer
type ElasticWriter struct {
	Client *elk.Client
	Index  string

	// debug toggles the log level of operational (per-bulk, per-file, periodic
	// metrics) messages. When true they are emitted at Info; when false they
	// are emitted at Debug and are invisible unless global --debug is set.
	debug bool

	queue    chan *queueItem
	wg       sync.WaitGroup
	closed   atomic.Bool
	failures atomic.Int64

	// Pending bulk buffers, keyed by destination index. They deliberately span
	// files: most files carry a handful of leaks, so flushing at every file
	// boundary (as the writer used to) meant ELK_BULK_SIZE never applied and a
	// small file cost up to eleven near-empty HTTP round-trips. Accumulating
	// across files makes the configured bulk size real.
	//
	// pendUpdates holds leak upserts (bulk "update", merged on the leak _id --
	// see pendingLeak); pendIndexes holds reference and file/_ctrl documents
	// (bulk "index", last write wins on the _id, which is deterministic). The
	// two never share an index name, so pendBytes tracks both.
	//
	// pendTotal is the sum of pendBytes. Buffers are per index and a run can
	// touch several (the leak families, _ctrl, and one reference index per
	// import month), so the per-index threshold alone bounds memory only when
	// multiplied by however many indices happen to be active. pendTotal caps
	// the whole set.
	// gate parks the writers while the cluster is refusing writes.
	gate clusterGate

	bulkMu      sync.Mutex
	pendUpdates map[string]map[string]*pendingLeak
	pendIndexes map[string]map[string][]byte
	pendBytes   map[string]int
	pendTotal   int

	// refIndexes memoises which monthly reference indices (intelparser_ref_YYYY-MM)
	// have already been created this run, so writeSync doesn't issue an Exists/Create
	// round-trip for every file. Keyed by index name -> struct{}{}.
	refIndexes sync.Map
	// refMu serializes the create-if-absent section of ensureRefIndex so two
	// concurrent workers hitting the first file of a new month don't both race
	// to create the same index (which would 400 with resource_already_exists).
	refMu sync.Mutex

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
	metDocErrs     atomic.Int64 // documents the cluster rejected permanently

	// Last values reported by the periodic metrics snapshot, so an idle writer
	// does not repeat an identical line every interval.
	lastMetFiles atomic.Int64
	lastMetBulks atomic.Int64

	startedAt    time.Time
	stopReporter chan struct{}
	reporterWG   sync.WaitGroup
}

// bulkItemResult mirrors the per-document result Elasticsearch returns inside a
// bulk response. The response keys each item by the action used ("index" or
// "update"), so both are decoded and the non-empty one is inspected.
type bulkItemResult struct {
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
}

type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index  bulkItemResult `json:"index"`
		Update bulkItemResult `json:"update"`
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
	base *http.Transport
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
// NewElasticClient builds a client for the cluster addressed by uri and returns
// it together with the base index name the URI path carries (defaulting to
// "intelparser"). It only talks to the cluster to ping it, so it is also the way
// to reach an index that is being read rather than written -- nothing here
// creates or alters an index.
func NewElasticClient(uri string, debug bool) (*elk.Client, string, error) {

	u, err := url.Parse(uri)
	if err != nil {
		return nil, "", err
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

	conf := elk.Config{
		Addresses: []string{
			fmt.Sprintf("%s://%s:%s/", u.Scheme, u.Hostname(), port),
		},
		//Username: username,
		//Password: password,
		//CACert:   cert,
		RetryOnStatus: []int{429, 502, 503, 504},
		MaxRetries:    5,
		RetryBackoff: func(i int) time.Duration {
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
		conf.Username = v1
		logger.Infof("Setting username %s using env.INTELPARSER_OUTPUT_USERNAME", v1)
	}
	if v1, ok := os.LookupEnv("INTELPARSER_OUTPUT_PASSWORD"); ok {
		conf.Password = v1
		logger.Infof("Setting password using env.INTELPARSER_OUTPUT_PASSWORD")
	}

	if username != "" && password != "" {
		conf.Username = username
		conf.Password = password
	}

	client, err := elk.NewClient(conf)
	if err != nil {
		return nil, "", err
	}

	// Faz um ping (chama GET / internamente)
	res, err := client.Ping()
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()

	return client, index_name, nil
}

func NewElasticWriter(uri string, debug bool) (*ElasticWriter, error) {

	client, index_name, err := NewElasticClient(uri, debug)
	if err != nil {
		return nil, err
	}

	wr := &ElasticWriter{
		Index:       index_name,
		Client:      client,
		debug:       debug,
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}

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

	if v1, ok := os.LookupEnv("ELK_METRICS_INTERVAL"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil && i1 >= 0 {
			logger.Infof("Setting ELK metrics interval to %ds using env.ELK_METRICS_INTERVAL", i1)
			elkMetricsInterval = time.Duration(i1) * time.Second
		}
	}

	if v1, ok := os.LookupEnv("ELK_PAUSE_INTERVAL"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil && i1 >= 1 {
			logger.Infof("Setting ELK pause retry interval to %ds using env.ELK_PAUSE_INTERVAL", i1)
			elkPauseInterval = time.Duration(i1) * time.Second
		}
	}

	if v1, ok := os.LookupEnv("ELK_CODEC"); ok && v1 != "" {
		logger.Infof("Setting ELK index.codec to %s using env.ELK_CODEC", v1)
		elkCodec = v1
	}

	shardsFromEnv("ELK_CTRL_SHARDS", &elkCtrlShards)
	shardsFromEnv("ELK_CREDS_SHARDS", &elkCredsShards)
	shardsFromEnv("ELK_URLS_SHARDS", &elkUrlsShards)
	shardsFromEnv("ELK_EMAILS_SHARDS", &elkEmailsShards)
	shardsFromEnv("ELK_PHONE_SHARDS", &elkPhoneShards)
	shardsFromEnv("ELK_DOCS_SHARDS", &elkDocsShards)
	shardsFromEnv("ELK_REFS_SHARDS", &elkRefsShards)

	// File/control index (global, single): file metadata only. The document _id
	// is the file fingerprint, so the fingerprint is no longer stored as a
	// field. Named <index>_ctrl so the bare <index> name stays free.
	err = wr.CreateIndex(wr.Index+"_ctrl", buildIndexBody(elkCtrlShards, `{
                    "indexed_at": {"type": "date"},
                    "leak_date": {"type": "date"},
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
                }`))
	if err != nil {
		return nil, err
	}

	// Leak indices (global, single, deduplicated): each holds ONLY the intrinsic
	// leak value plus two dates — inserted_at (first time it was ever seen) and
	// last_reference_at (most recent import that referenced it). No file
	// reference, no occurrence context, no fingerprint field: the _id is the
	// content hash (models.LeakIndexable.LeakID), which globally dedups the leak.

	//Credential Index
	err = wr.CreateIndex(wr.Index+"_creds", buildIndexBody(elkCredsShards, `{
                    "inserted_at": {"type": "date"},
                    "last_reference_at": {"type": "date"},
                    "rule": {"type": "keyword"},
                    "user_domain": {"type": "keyword"},
                    "username": {"type": "keyword"},
                    "password": {"type": "keyword"},
                    "cpf": {"type": "keyword"},
                    "url": {"type": "keyword"},
                    "url_domain": {"type": "keyword"},
                    "severity": {"type": "long"},
                    "entropy": {"type": "float"}
                }`))
	if err != nil {
		return nil, err
	}

	//Urls Index
	err = wr.CreateIndex(wr.Index+"_urls", buildIndexBody(elkUrlsShards, `{
                    "inserted_at": {"type": "date"},
                    "last_reference_at": {"type": "date"},
                    "domain": {"type": "keyword"},
                    "url": {"type": "keyword"}
                }`))
	if err != nil {
		return nil, err
	}

	//Emails Index
	err = wr.CreateIndex(wr.Index+"_emails", buildIndexBody(elkEmailsShards, `{
                    "inserted_at": {"type": "date"},
                    "last_reference_at": {"type": "date"},
                    "domain": {"type": "keyword"},
                    "email": {"type": "keyword"}
                }`))
	if err != nil {
		return nil, err
	}

	//Phone Index
	err = wr.CreateIndex(wr.Index+"_phone", buildIndexBody(elkPhoneShards, `{
                    "inserted_at": {"type": "date"},
                    "last_reference_at": {"type": "date"},
                    "country": {"type": "keyword"},
                    "raw": {"type": "text"},
                    "phone": {"type": "keyword"}
                }`))
	if err != nil {
		return nil, err
	}

	//Document Index (CPF / CNPJ)
	err = wr.CreateIndex(wr.Index+"_document", buildIndexBody(elkDocsShards, `{
                    "inserted_at": {"type": "date"},
                    "last_reference_at": {"type": "date"},
                    "raw": {"type": "text"},
                    "number": {"type": "keyword"},
                    "is_cpf": {"type": "boolean"},
                    "is_cnpj": {"type": "boolean"}
                }`))
	if err != nil {
		return nil, err
	}

	// Apply ingest-friendly settings to all managed static indices (new and
	// existing). Monthly reference indices are created lazily per import month
	// (see ensureRefIndex), which applies the same settings on creation.
	for _, idx := range []string{wr.Index + "_ctrl", wr.Index + "_creds", wr.Index + "_urls", wr.Index + "_emails", wr.Index + "_phone", wr.Index + "_document"} {
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

// buildIndexBody wraps a mappings "properties" object (passed as a raw JSON
// string, braces included) with the standard creation-time settings shared by
// every managed index. The store codec is injected here on purpose: index.codec
// is a static setting that can only be set at creation time, never patched on an
// open index — so unlike refresh_interval / translog.durability (handled live by
// applyIngestSettings) it MUST live in the create body. New indices are
// therefore born with best_compression (or whatever ELK_CODEC sets), avoiding a
// later reindex/force_merge just to reclaim disk. number_of_shards is static for
// the same reason, and is passed in per index family rather than fixed here —
// see the elk*Shards vars.
//
// number_of_replicas is the odd one out: it is dynamic, but is set here as well
// so a new index is born with the right count instead of being created at the
// cluster default and corrected a moment later by applyIngestSettings.
func buildIndexBody(shards int, properties string) string {
	return fmt.Sprintf(`{
            "settings": {
                "number_of_shards": %d,
                "number_of_replicas": %d,
                "index": {
                    "highlight.max_analyzed_offset": 10000000,
                    "codec": %q
                }
            },
            "mappings": {
                "properties": %s
            }
        }`, shards, elkReplicas, elkCodec, properties)
}

// shardsFromEnv applies a per-index number_of_shards override from the
// environment. A missing, non-numeric or out-of-range value leaves the default
// untouched and warns instead of failing: a typo here would otherwise be
// baked into an index that can only be resharded by reindexing it.
func shardsFromEnv(envName string, target *int) {
	v1, ok := os.LookupEnv(envName)
	if !ok {
		return
	}
	i1, err := strconv.ParseInt(v1, 10, 32)
	if err != nil || i1 < 1 || i1 > 1024 {
		logger.Warnf("Ignoring env.%s=%q: number_of_shards must be an integer between 1 and 1024", envName, v1)
		return
	}
	logger.Infof("Setting ELK number_of_shards to %d using env.%s", i1, envName)
	*target = int(i1)
}

// ingestSettingsBody builds the dynamic settings patch applied to every managed
// index. number_of_replicas is included only when elkReplicasUpdate opts in;
// see that var for why a routine import must not rewrite it.
func ingestSettingsBody() map[string]interface{} {
	idx := map[string]interface{}{
		"refresh_interval": elkRefreshInterval,
		"translog": map[string]interface{}{
			"durability": elkTranslogDurability,
		},
	}
	if elkReplicasUpdate >= 0 {
		idx["number_of_replicas"] = elkReplicasUpdate
	}
	return map[string]interface{}{"index": idx}
}

// applyIngestSettings tunes an index for bulk ingestion throughput.
// Applied once on writer init so it also updates existing indices.
func (ew *ElasticWriter) applyIngestSettings(index string) error {
	b, err := json.Marshal(ingestSettingsBody())
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

// refIndexName returns the monthly reference index name for a given import
// timestamp, e.g. "intelparser_ref_2026-07". Partitioning is by import
// (indexed_at) month so whole import batches can be rotated/dropped as a unit.
func (ew *ElasticWriter) refIndexName(indexedAt time.Time) string {
	return fmt.Sprintf("%s_ref_%s", ew.Index, indexedAt.UTC().Format("2006-01"))
}

// ensureRefIndex lazily creates the monthly file<->leak reference index the
// first time it is needed in this run, then memoises it so subsequent files in
// the same month skip the Exists/Create/settings round-trips. The reference
// document carries the pointer (file_id, leak_id, type) plus the
// occurrence-specific context (bucket, near_text, line, source, file_name)
// that does not belong on the deduplicated leak itself.
func (ew *ElasticWriter) ensureRefIndex(index string) error {
	if _, ok := ew.refIndexes.Load(index); ok {
		return nil
	}

	// Serialize creation and re-check under the lock: only the first worker to
	// arrive for a given month actually creates the index; the rest see it in
	// the map and fall through.
	ew.refMu.Lock()
	defer ew.refMu.Unlock()
	if _, ok := ew.refIndexes.Load(index); ok {
		return nil
	}

	err := ew.CreateIndex(index, buildIndexBody(elkRefsShards, `{
                    "indexed_at": {"type": "date"},
                    "file_id": {"type": "keyword"},
                    "leak_id": {"type": "keyword"},
                    "type": {"type": "keyword"},
                    "bucket": {"type": "text"},
                    "near_text": {"type": "text"},
                    "source": {"type": "keyword"},
                    "file_name": {"type": "keyword"},
                    "line": {"type": "text"}
                }`))
	if err != nil {
		return err
	}

	if err := ew.applyIngestSettings(index); err != nil {
		logger.Warnf("Could not apply ingest settings to %s: %s", index, err)
	}

	ew.refIndexes.Store(index, struct{}{})
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

	// Workers are done, so nothing can add to the bulk buffers any more: send
	// whatever is still pending in them before reporting the run finished.
	if err := ew.flushPending(); err != nil {
		ew.failures.Add(1)
		logger.Errorf("Elastic writer failure flushing pending bulks: %s", err)
	}

	// Stop the metrics reporter and emit a final summary.
	close(ew.stopReporter)
	ew.reporterWG.Wait()
	ew.logMetrics(true)

	if n := ew.failures.Load(); n > 0 {
		logger.Warnf("ELK writer finished with %d failure(s)", n)
	}
	return nil
}

// queueLeak buffers one leak upsert for leakIndex, coalescing it with any
// occurrence of the same leak already pending: the intrinsic document is
// identical (the _id is its content hash), so only the date range widens. That
// keeps the earliest inserted_at even though the buffer now spans files, and it
// collapses repeats of a popular leak into a single update instead of several.
//
// The buffer is detached under the lock and sent outside it, so a flush never
// blocks the other workers filling other indices.
func (ew *ElasticWriter) queueLeak(leakIndex, leakID string, doc []byte, ts string) error {
	ew.bulkMu.Lock()
	m := ew.pendUpdates[leakIndex]
	if m == nil {
		m = map[string]*pendingLeak{}
		ew.pendUpdates[leakIndex] = m
	}
	if p, ok := m[leakID]; ok {
		if ts < p.first {
			p.first = ts
		}
		if ts > p.last {
			p.last = ts
		}
	} else {
		m[leakID] = &pendingLeak{doc: doc, first: ts, last: ts}
		// Exactly what this leak will add to the payload: the document, the
		// _id, and the fixed-width update envelope around both.
		n := len(doc) + len(leakID) + elkLeakLineOverhead
		ew.pendBytes[leakIndex] += n
		ew.pendTotal += n
	}
	var detached map[string]*pendingLeak
	if len(m) >= elkBulkCount || ew.pendBytes[leakIndex] >= elkBulkMaxSize {
		detached = m
		ew.pendTotal -= ew.pendBytes[leakIndex]
		delete(ew.pendUpdates, leakIndex)
		delete(ew.pendBytes, leakIndex)
	}
	spill := ew.overBudgetLocked()
	ew.bulkMu.Unlock()

	var firstErr error
	if detached != nil {
		firstErr = ew.sendLeakBulk(leakIndex, detached)
	}
	if err := ew.sendDetached(spill); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// queueDoc buffers one plain document (a reference doc, or a file document for
// the _ctrl index) for a bulk "index" action. Ids are deterministic, so a
// repeat inside the buffer is the same document and simply overwrites.
func (ew *ElasticWriter) queueDoc(index, id string, doc []byte) error {
	n := len(doc) + len(id) + elkDocLineOverhead

	// A document that on its own exceeds the whole bulk budget cannot be split
	// -- a file's _ctrl document carries the file's entire content, and a large
	// leak file runs to tens of MB. Buffering it anyway would put it on the
	// wire together with up to ELK_BULK_SIZE other documents, so the one
	// request that was already too big gets bigger. Send it alone instead, and
	// drop any copy of it still pending so the buffer cannot flush a superseded
	// version over the one just written.
	if n >= elkBulkMaxSize {
		ew.bulkMu.Lock()
		if prev, ok := ew.pendIndexes[index][id]; ok {
			was := len(prev) + len(id) + elkDocLineOverhead
			ew.pendBytes[index] -= was
			ew.pendTotal -= was
			delete(ew.pendIndexes[index], id)
		}
		spill := ew.overBudgetLocked()
		ew.bulkMu.Unlock()

		err := ew.sendBulk(index, "index", map[string][]byte{id: doc})
		if serr := ew.sendDetached(spill); serr != nil && err == nil {
			err = serr
		}
		return err
	}

	ew.bulkMu.Lock()
	m := ew.pendIndexes[index]
	if m == nil {
		m = map[string][]byte{}
		ew.pendIndexes[index] = m
	}
	if prev, ok := m[id]; ok {
		// Undo the whole previous entry, envelope included, or a replayed id
		// inflates the budget every time it comes round again.
		was := len(prev) + len(id) + elkDocLineOverhead
		ew.pendBytes[index] -= was
		ew.pendTotal -= was
	}
	m[id] = doc
	ew.pendBytes[index] += n
	ew.pendTotal += n
	var detached map[string][]byte
	if len(m) >= elkBulkCount || ew.pendBytes[index] >= elkBulkMaxSize {
		detached = m
		ew.pendTotal -= ew.pendBytes[index]
		delete(ew.pendIndexes, index)
		delete(ew.pendBytes, index)
	}
	spill := ew.overBudgetLocked()
	ew.bulkMu.Unlock()

	var firstErr error
	if detached != nil {
		firstErr = ew.sendBulk(index, "index", detached)
	}
	if err := ew.sendDetached(spill); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// pendingBatch is a detached set of buffers on its way out, so the sending
// happens outside the lock.
type pendingBatch struct {
	updates map[string]map[string]*pendingLeak
	indexes map[string]map[string][]byte
}

// overBudgetLocked detaches every buffer once the writer as a whole is holding
// more than four bulks worth of pending documents. Must be called with bulkMu
// held. Returns nil while the writer is within budget.
func (ew *ElasticWriter) overBudgetLocked() *pendingBatch {
	if ew.pendTotal < 4*elkBulkMaxSize {
		return nil
	}
	b := &pendingBatch{updates: ew.pendUpdates, indexes: ew.pendIndexes}
	ew.pendUpdates = map[string]map[string]*pendingLeak{}
	ew.pendIndexes = map[string]map[string][]byte{}
	ew.pendBytes = map[string]int{}
	ew.pendTotal = 0
	return b
}

// sendDetached ships a set of buffers detached by overBudgetLocked / flushPending.
func (ew *ElasticWriter) sendDetached(b *pendingBatch) error {
	if b == nil {
		return nil
	}
	var firstErr error
	for index, docs := range b.updates {
		if err := ew.sendLeakBulk(index, docs); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for index, docs := range b.indexes {
		if err := ew.sendBulk(index, "index", docs); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// flushPending sends every buffered bulk. Called once by Flush after the
// workers have drained, which is what makes the cross-file buffering safe: no
// document can be left behind in a buffer at the end of a run.
func (ew *ElasticWriter) flushPending() error {
	ew.bulkMu.Lock()
	b := &pendingBatch{updates: ew.pendUpdates, indexes: ew.pendIndexes}
	ew.pendUpdates = map[string]map[string]*pendingLeak{}
	ew.pendIndexes = map[string]map[string][]byte{}
	ew.pendBytes = map[string]int{}
	ew.pendTotal = 0
	ew.bulkMu.Unlock()

	return ew.sendDetached(b)
}

// recordBulk atomically updates bulk-level metrics. Called by sendBulk on
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
	if elkMetricsInterval <= 0 {
		<-ew.stopReporter
		return
	}
	ticker := time.NewTicker(elkMetricsInterval)
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
	files := ew.metFiles.Load()

	// An idle writer would otherwise repeat an identical line every interval;
	// only report a periodic snapshot once a file or a bulk has landed. A
	// stalled cluster is the exception: nothing moves precisely because the
	// writers are parked, which is the one moment the line matters most.
	if !final && ew.gate.pausedFor() == 0 &&
		files == ew.lastMetFiles.Load() && bulks == ew.lastMetBulks.Load() {
		return
	}
	ew.lastMetFiles.Store(files)
	ew.lastMetBulks.Store(bulks)

	docs := ew.metDocs.Load()
	bytes := ew.metBytes.Load()
	latSum := ew.metLatencyNs.Load()
	latMax := ew.metLatencyMax.Load()
	ftime := ew.metFileTimeNs.Load()
	qwait := ew.metQueueWaitNs.Load()
	errs := ew.failures.Load()
	retries := ew.metBulkRetries.Load()
	docErrs := ew.metDocErrs.Load()
	pausedTotal := time.Duration(ew.gate.total.Load()) + ew.gate.pausedFor()

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

	// Both the periodic snapshot and the final summary go out at Info: these
	// counters are the only in-flight view of where a long import is spending
	// its time, and hiding them behind --write-elasticsearch-enable-debug meant
	// paying for per-bulk log spam to get them. Set ELK_METRICS_INTERVAL=0 to
	// turn the periodic line off.
	tag := "ELK metrics"
	emit := logger.Infof
	if final {
		tag = "ELK final metrics"
	}

	// A run that spent time parked on a refusing cluster has a misleading
	// docs/s unless the pause is visible next to it.
	state := ""
	if d := ew.gate.pausedFor(); d > 0 {
		state = fmt.Sprintf(" PAUSED(%s)", d.Round(time.Second))
	}

	emit("%s:%s queue=%d/%d files=%d (%.1f/s) bulks=%d docs=%d (%.0f/s) bytes=%s (%s/s) avg_bulk=%s max_bulk=%s avg_file=%s avg_queue_wait=%s paused=%s retries=%d errs=%d doc_errs=%d",
		tag, state,
		qlen, qcap,
		files, float64(files)/elapsed,
		bulks,
		docs, float64(docs)/elapsed,
		tools.Bytes(uint64(bytes)), tools.Bytes(uint64(float64(bytes)/elapsed)),
		avgBulk, time.Duration(latMax),
		avgFile, avgQWait,
		pausedTotal.Round(time.Second),
		retries, errs, docErrs,
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

	top := "┌" + strings.Repeat("─", kw) + "┬" + strings.Repeat("─", vw) + "┐"
	sep := "├" + strings.Repeat("─", kw) + "┼" + strings.Repeat("─", vw) + "┤"
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
		b.WriteString(title)
		b.WriteString("\n")
	}
	b.WriteString(top)
	b.WriteString("\n")
	b.WriteString("│")
	b.WriteString(center(keyHeader, kw))
	b.WriteString("│")
	b.WriteString(center(valHeader, vw))
	b.WriteString("│\n")
	b.WriteString(sep)
	b.WriteString("\n")
	// Data rows are emitted back-to-back without a separator between them —
	// keeps the table compact, especially when the surrounding pipeline
	// prefixes every line with a timestamp.
	for _, r := range rows {
		b.WriteString("│")
		b.WriteString(left(r[0], kw))
		b.WriteString("│")
		b.WriteString(left(r[1], vw))
		b.WriteString("│\n")
	}
	b.WriteString(bottom)
	b.WriteString("\n")
	return b.String()
}

// refID is the deterministic _id of a reference document: it ties one file to
// one leak, so the same (file, leak) pair is idempotent across re-imports.
func refID(fileID, leakID string) string {
	var hash string
	models.CalcRefHash(&hash, fileID, leakID)
	return hash
}

// ingestLeaks handles one leak type for one file. For every leak it queues:
//
//   - a leak-index upsert (bulk "update") keyed by the content hash. The first
//     sighting inserts the intrinsic value with inserted_at =
//     last_reference_at = the file's import time; later sightings run
//     elkLeakDateScript, which widens that range instead of overwriting it, so
//     the result no longer depends on the order the updates are applied in.
//     retry_on_conflict (set in sendLeakBulk) absorbs the version conflicts
//     concurrent workers upserting the same shared leak would raise.
//   - a reference-index document (bulk "index") keyed by refID, holding the
//     file<->leak pointer plus the occurrence context.
//
// Nothing is flushed here: both go into the writer-wide buffers, which span
// files and flush on elkBulkCount / elkBulkMaxSize (see queueLeak / queueDoc).
func ingestLeaks[T models.LeakIndexable](ew *ElasticWriter, leakIndex, refIndex,
	fileID, bucket string, indexedAt time.Time, items []T) error {

	if len(items) == 0 {
		return nil
	}
	nowStr := indexedAt.UTC().Format(time.RFC3339)

	for _, it := range items {
		leakID := it.LeakID()

		lb, err := json.Marshal(it.LeakDoc())
		if err != nil {
			return err
		}
		if err := ew.queueLeak(leakIndex, leakID, lb, nowStr); err != nil {
			return err
		}

		// Reference doc: pointer + occurrence context.
		ref := it.RefDoc()
		ref["indexed_at"] = nowStr
		ref["file_id"] = fileID
		ref["leak_id"] = leakID
		ref["type"] = it.LeakType()
		ref["bucket"] = bucket
		rb, err := json.Marshal(ref)
		if err != nil {
			return err
		}
		if err := ew.queueDoc(refIndex, refID(fileID, leakID), rb); err != nil {
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
	ew.logf("Integrating elastic (file=%s): %d credentials, %d e-mails, %d urls, %d phones, %d documents",
		result.FileName, len(result.Credentials), len(result.Emails), len(result.URLs), len(result.Phones), len(result.Documents))

	// Partition the reference index by import (indexed_at) month. Fall back to
	// the leak date, then to now, so a File that reaches the writer without an
	// indexed_at (e.g. some conversion paths) still lands in a sane partition.
	indexedAt := result.IndexedAt
	if indexedAt.IsZero() {
		indexedAt = result.Date
	}
	if indexedAt.IsZero() {
		indexedAt = time.Now()
	}
	refIndex := ew.refIndexName(indexedAt)
	if err := ew.ensureRefIndex(refIndex); err != nil {
		return err
	}

	fileID := result.Fingerprint

	var wg sync.WaitGroup
	errs := make([]error, 5)

	wg.Add(5)
	go func() {
		defer wg.Done()
		errs[0] = ingestLeaks(ew, ew.Index+"_creds", refIndex,
			fileID, result.Bucket, indexedAt, result.Credentials)
	}()
	go func() {
		defer wg.Done()
		errs[1] = ingestLeaks(ew, ew.Index+"_urls", refIndex,
			fileID, result.Bucket, indexedAt, result.URLs)
	}()
	go func() {
		defer wg.Done()
		errs[2] = ingestLeaks(ew, ew.Index+"_emails", refIndex,
			fileID, result.Bucket, indexedAt, result.Emails)
	}()
	go func() {
		defer wg.Done()
		errs[3] = ingestLeaks(ew, ew.Index+"_phone", refIndex,
			fileID, result.Bucket, indexedAt, result.Phones)
	}()
	go func() {
		defer wg.Done()
		errs[4] = ingestLeaks(ew, ew.Index+"_document", refIndex,
			fileID, result.Bucket, indexedAt, result.Documents)
	}()
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return e
		}
	}

	// File doc — build a local copy without the heavy slices so the caller's
	// File (and any other writers sharing the pointer) are not mutated. Routed
	// through ew.Marshal so the fingerprint field (now the document _id) and the
	// other excluded fields are stripped from the stored document.
	fileDoc := *result
	fileDoc.Credentials = nil
	fileDoc.Emails = nil
	fileDoc.URLs = nil
	fileDoc.Phones = nil
	fileDoc.Documents = nil

	b_data, err := ew.Marshal(fileDoc)
	if err != nil {
		return err
	}

	// Buffered like everything else rather than sent as its own request: one
	// single-document index call per file was, for the many files that carry
	// few leaks, the largest single share of the round-trips.
	return ew.queueDoc(ew.Index+"_ctrl", fileID, b_data)
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
				Body:  strings.NewReader(string(mapping)),
			}

			logger.Infof("Creating elastic index %s", index)
			res, err := indexReq.Do(context.Background(), ew.Client)
			if err != nil {
				return err
			}
			defer res.Body.Close()

			if res.IsError() {

				if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
					return fmt.Errorf("Failure to to parse response body: %s", err)
				} else {
					errType, _ := raw["error"].(map[string]interface{})["type"].(string)
					// A concurrent creation may have won the race; that is not a
					// failure — the index we wanted now exists.
					if errType == "resource_already_exists_exception" {
						return nil
					}
					return fmt.Errorf("Cannot create/update elastic index [%d] %s: %s",
						res.StatusCode,
						raw["error"].(map[string]interface{})["type"],
						raw["error"].(map[string]interface{})["reason"])
				}

			}

		} else {

			if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
				return fmt.Errorf("Failure to to parse response body: %s", err)
			} else {
				return fmt.Errorf("Cannot get elastic index [%d] %s: %s",
					response.StatusCode,
					raw["error"].(map[string]interface{})["type"],
					raw["error"].(map[string]interface{})["reason"])
			}

		}

	}

	return nil

}

// jsonQuote returns s as a JSON string literal (quoted and escaped).
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// leakUpsertLine renders the update body for one pending leak: the date script
// for documents that already exist, and the seed document (intrinsic fields
// plus both dates) for the first insertion.
//
// The two dates are spliced onto the front of the already-marshalled intrinsic
// object instead of re-marshalling it, because the object is built once when
// the leak enters the buffer while the dates keep moving until it is flushed.
func leakUpsertLine(p *pendingLeak) []byte {
	var seed []byte
	head := fmt.Sprintf(`{"inserted_at":%s,"last_reference_at":%s`, jsonQuote(p.first), jsonQuote(p.last))
	if len(p.doc) < 2 || p.doc[0] != '{' || len(p.doc) == 2 {
		seed = []byte(head + "}")
	} else {
		seed = append([]byte(head+","), p.doc[1:]...)
	}
	return []byte(fmt.Sprintf(`{"script":{"lang":"painless","source":%s,"params":{"first":%s,"last":%s}},"upsert":%s}`,
		elkLeakScriptJSON, jsonQuote(p.first), jsonQuote(p.last), seed))
}

// bulkUpdateMeta renders the action line of one buffered leak upsert.
// retry_on_conflict lets concurrent workers touching the same shared leak retry
// instead of failing with a version conflict.
func bulkUpdateMeta(id string) string {
	return fmt.Sprintf(`{ "update" : { "_id" : %s, "retry_on_conflict" : 5 } }%s`, jsonQuote(id), "\n")
}

// bulkDocMeta renders the action line of one buffered full document.
func bulkDocMeta(action, id string) string {
	return fmt.Sprintf(`{ %s : { "_id" : %s } }%s`, jsonQuote(action), jsonQuote(id), "\n")
}

// elkLeakLineOverhead and elkDocLineOverhead are the exact number of bytes one
// buffered document adds to a bulk payload beyond its own body and its _id: the
// action line, and for a leak the script/params envelope leakUpsertLine wraps
// the document in.
//
// They are constants because everything variable in them is fixed-width: both
// leak timestamps are RFC3339 UTC (a 20-character layout with no fractional
// part), and the painless script is the same 285 bytes on every single line.
//
// They are measured from the real renderers rather than written down, because a
// written-down number is what ELK_BULK_BYTES used to be enforced with: the
// estimate said 220 bytes per leak while the line actually costs 546, so a
// budget of 5 MB was producing bulk requests of roughly 9 MB. A bulk budget
// that does not match the bytes on the wire is not a budget.
var (
	elkLeakLineOverhead = measureLeakLineOverhead()
	elkDocLineOverhead  = len(bulkDocMeta("index", "")) + 1 // + the newline after the body
)

// measureLeakLineOverhead renders one leak line with a known body and subtracts
// the body, leaving the per-line cost of the envelope. The _id is empty, so the
// two quotes jsonQuote adds around it are part of the overhead and callers add
// only len(id).
func measureLeakLineOverhead() int {
	ts := time.Time{}.UTC().Format(time.RFC3339)
	// A non-degenerate body: leakUpsertLine splices the dates into it, which
	// costs one byte more than the empty-object branch it would otherwise take.
	body := []byte(`{"a":1}`)
	p := &pendingLeak{doc: body, first: ts, last: ts}
	return len(bulkUpdateMeta("")) + len(leakUpsertLine(p)) + 1 - len(body)
}

// bulkLine is one document's contribution to a bulk request: the action
// metadata and the body, already rendered as the two NDJSON lines they will be
// sent as. Keeping them per document and in order is what lets a partially
// failed bulk be retried with only the documents that failed -- Elasticsearch
// returns the items in request order, so item i belongs to line i.
type bulkLine struct {
	id     string
	ndjson []byte
}

// renderBulk concatenates lines into a bulk request payload.
func renderBulk(lines []bulkLine) []byte {
	size := 0
	for _, l := range lines {
		size += len(l.ndjson)
	}
	buf := make([]byte, 0, size)
	for _, l := range lines {
		buf = append(buf, l.ndjson...)
	}
	return buf
}

// sendLeakBulk ships a batch of deduplicated leak upserts to a leak index.
// retry_on_conflict lets concurrent workers touching the same shared leak retry
// instead of failing with a version conflict.
func (ew *ElasticWriter) sendLeakBulk(index string, docs map[string]*pendingLeak) error {
	if len(docs) == 0 {
		return nil
	}
	lines := make([]bulkLine, 0, len(docs))
	for id, p := range docs {
		meta := bulkUpdateMeta(id)
		body := leakUpsertLine(p)
		nd := make([]byte, 0, len(meta)+len(body)+1)
		nd = append(nd, meta...)
		nd = append(nd, body...)
		nd = append(nd, '\n')
		lines = append(lines, bulkLine{id: id, ndjson: nd})
	}
	return ew.postBulk(index, "update", lines)
}

// sendBulk ships a batch of full source documents to `index`. The meta line is
// { "index": { "_id": <id> } }, so an existing document is replaced -- every id
// this writer produces is deterministic, so a replay rewrites the same content.
func (ew *ElasticWriter) sendBulk(index string, action string, docs map[string][]byte) error {
	if len(docs) == 0 {
		return nil
	}
	lines := make([]bulkLine, 0, len(docs))
	for id, doc := range docs {
		meta := bulkDocMeta(action, id)
		nd := make([]byte, 0, len(meta)+len(doc)+1)
		nd = append(nd, meta...)
		nd = append(nd, doc...)
		nd = append(nd, '\n')
		lines = append(lines, bulkLine{id: id, ndjson: nd})
	}
	return ew.postBulk(index, action, lines)
}

// retriableBulkStatus reports whether a status code is back-pressure or a
// transient unavailability rather than a verdict on the request itself.
func retriableBulkStatus(status int) bool {
	switch status {
	case 408, 409, 429, 502, 503, 504:
		return true
	}
	return false
}

// clusterRefusing reports whether a per-document failure means the cluster as
// a whole cannot take writes right now -- a disk-watermark block, a rejected
// execution, a tripped circuit breaker, a shard that is not available. These
// are the failures worth parking every writer for, because every writer is
// about to hit them too.
func clusterRefusing(r bulkItemResult) bool {
	switch r.Status {
	case 429, 502, 503, 504:
		return true
	}
	switch r.Error.Type {
	case "cluster_block_exception", "es_rejected_execution_exception",
		"circuit_breaking_exception", "unavailable_shards_exception",
		"no_shard_available_action_exception":
		return true
	}
	return false
}

// retriableBulkItem reports whether a per-document failure is worth waiting out
// rather than dropping the document: anything the cluster is refusing, plus a
// version conflict (408/409), which is local contention that a later attempt
// merges correctly rather than a reason to park the whole pipeline. A mapping
// error or a malformed document, by contrast, fails identically on every retry,
// so retrying it forever would stall the run over a document that can never
// land.
func retriableBulkItem(r bulkItemResult) bool {
	return retriableBulkStatus(r.Status) || clusterRefusing(r)
}

// postBulk performs the HTTP _bulk call for an already-rendered set of lines
// and does not return until every one of them has either been written or
// failed in a way that retrying cannot fix.
//
// A bulk request answering 200 does not mean the documents were written: the
// per-item results carry their own status, and a cluster refusing writes (say,
// a flood-stage disk watermark putting the index in read-only-allow-delete)
// answers 200 with every single item failing 429. Treating that as success is
// silent data loss, which is why failed items are collected and re-sent rather
// than counted and logged.
//
// While the cluster is refusing writes there is no point in sixteen workers
// discovering that independently, so the first one to see it holds the gate:
// it alone re-probes on an interval, everyone else parks until it succeeds.
// The queue backs up behind them and the readers block on it, so the whole
// pipeline pauses in place, with nothing buffered being dropped.
func (ew *ElasticWriter) postBulk(index string, action string, lines []bulkLine) error {
	if len(lines) == 0 {
		return nil
	}

	pending := lines
	total := len(lines)
	start := time.Now()

	// Set while this goroutine is the one probing a paused cluster; the defer
	// makes sure the gate is released on every exit path, error included.
	owner := false
	defer func() {
		if owner {
			ew.gate.resume()
		}
	}()

	logPaused := time.Time{}
	hardFails := 0
	softFails := 0
	permTotal := 0

	for attempt := 0; ; attempt++ {
		if !owner {
			// Non-owners park here for as long as the cluster is refusing
			// writes; the owner must not, or it would wait on its own gate.
			ew.gate.wait()
		}

		var (
			retry       []bulkLine
			reason      string
			clusterWide bool
			perm        int
			permEx      string
		)

		payload := renderBulk(pending)
		reqStart := time.Now()
		res, err := ew.Client.Bulk(bytes.NewReader(payload), ew.Client.Bulk.WithIndex(index))
		reqDur := time.Since(reqStart)
		if attempt > 0 {
			ew.metBulkRetries.Add(1)
		}

		if err != nil {
			// The cluster is unreachable and the client has already exhausted
			// its own retries. Returning here would drop the batch, so an
			// unreachable cluster is treated exactly like a refusing one: hold
			// the documents and wait for it to come back.
			retry, clusterWide = pending, true
			reason = fmt.Sprintf("%s is unreachable: %s", index, err)
		} else {
			status := res.StatusCode
			var blk bulkResponse
			decErr := json.NewDecoder(res.Body).Decode(&blk)
			drainClose(res.Body)

			switch {
			case status == 200 || status == 201:
				// A response that does not account for every document sent
				// cannot be trusted to say which ones landed. Re-sending is
				// safe -- every id is deterministic, so a document written
				// twice is written once -- while assuming success is not.
				if decErr != nil || len(blk.Items) != len(pending) {
					hardFails++
					if hardFails >= 5 {
						return fmt.Errorf("bulk on %s answered %d with %d results for %d documents (decode: %v)",
							index, status, len(blk.Items), len(pending), decErr)
					}
					logger.Warnf("Elastic bulk %s: unreadable response (%d results for %d documents); re-sending",
						index, len(blk.Items), len(pending))
					time.Sleep(time.Duration(hardFails) * time.Second)
					continue
				}

				for i, d := range blk.Items {
					// The item is keyed by the action used; pick whichever the
					// server populated (Status != 0).
					r := d.Index
					if r.Status == 0 {
						r = d.Update
					}
					if r.Status <= 201 {
						continue
					}
					if retriableBulkItem(r) {
						retry = append(retry, pending[i])
						if clusterRefusing(r) {
							clusterWide = true
						}
						if reason == "" {
							reason = fmt.Sprintf("%s: [%d] %s: %s", index, r.Status, r.Error.Type, r.Error.Reason)
						}
					} else {
						perm++
						if permEx == "" {
							permEx = fmt.Sprintf("[%d] %s: %s", r.Status, r.Error.Type, r.Error.Reason)
						}
					}
				}

				if perm > 0 {
					// Nothing a retry can do for these; report them loudly and
					// let the rest of the batch through rather than stalling
					// the whole run on a document that can never land.
					permTotal += perm
					ew.metDocErrs.Add(int64(perm))
					logger.Errorf("Elastic bulk %s: %d document(s) rejected permanently and dropped (first: %s)",
						index, perm, permEx)
				}

				if wrote := len(pending) - len(retry) - perm; wrote > 0 {
					ew.recordBulk(wrote, len(payload), reqDur)
				}

				if len(retry) == 0 {
					if owner {
						ew.resumeCluster()
						owner = false
					}
					ew.logf("Elastic bulk OK %s: %d/%d docs, %s in %s (req=%s)",
						index, total-permTotal, total, tools.Bytes(uint64(len(payload))), time.Since(start), reqDur)
					return nil
				}

			case retriableBulkStatus(status):
				retry, clusterWide = pending, status != 409
				reason = fmt.Sprintf("%s: the whole bulk was refused with status %d", index, status)

			default:
				// A verdict on the request itself (a malformed payload, a 4xx
				// that is not back-pressure). Re-sending the same bytes will
				// not change it, but give it a few goes in case it is transient.
				hardFails++
				if hardFails >= 5 {
					return fmt.Errorf("bulk on %s failed with status %d after %d attempts", index, status, hardFails)
				}
				ew.logf("Elastic bulk attempt %d on %s failed with status %d in %s; retrying",
					hardFails, index, status, reqDur)
				time.Sleep(time.Duration(hardFails) * time.Second)
				continue
			}
		}

		// Something retriable is left. Local contention (a version conflict
		// that outlived retry_on_conflict) is not a reason to park every other
		// writer -- back off briefly and re-send just those documents. Only
		// escalate to the shared pause when it will not clear on its own.
		pending = retry
		if !clusterWide {
			softFails++
			if softFails < 10 {
				time.Sleep(time.Duration(min(softFails, 5)) * 200 * time.Millisecond)
				continue
			}
			reason = fmt.Sprintf("%s: %d document(s) still conflicting after %d attempts",
				index, len(pending), softFails)
		}

		if !owner {
			owner = ew.gate.pause()
			if !owner {
				// Someone else owns the pause: loop back and park on the gate.
				continue
			}
			logger.Warnf("Elastic is refusing writes (%s); pausing all writers and retrying every %s",
				reason, elkPauseInterval)
			logPaused = time.Now()
		}

		// Owner: wait out the interval, then re-probe with the held documents.
		time.Sleep(elkPauseInterval)
		if time.Since(logPaused) >= time.Minute {
			logger.Warnf("Elastic still refusing writes after %s (%s); %d document(s) held, retrying",
				ew.gate.pausedFor().Round(time.Second), reason, len(pending))
			logPaused = time.Now()
		}
	}
}

// resumeCluster releases a pause this goroutine owned and reports how long the
// whole pipeline was held.
func (ew *ElasticWriter) resumeCluster() {
	if d := ew.gate.resume(); d > 0 {
		logger.Infof("Elastic accepted writes again after %s; resuming all writers", d.Round(time.Second))
	}
}

// drainClose reads a response body to EOF before closing it. Go only returns a
// keep-alive connection to the pool when its body was fully consumed; closing
// early makes the next request pay a fresh TCP (and, over HTTPS, TLS)
// handshake, which at hundreds of thousands of requests per run is not free.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
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
					return fmt.Errorf("Failure to to parse response body: %s", err)
				} else {
					return fmt.Errorf("Error: [%d] %s: %s",
						res.StatusCode,
						raw["error"].(map[string]interface{})["type"],
						raw["error"].(map[string]interface{})["reason"])
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
				return fmt.Errorf("Failure to to parse response body: %s", err)
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
