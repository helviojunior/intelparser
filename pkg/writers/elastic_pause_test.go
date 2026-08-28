package writers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	elk "github.com/elastic/go-elasticsearch/v8"
)

// itemVerdict is what the fake cluster answers for one document of one bulk.
type itemVerdict struct {
	status int
	typ    string
	reason string
}

var okItem = itemVerdict{status: 200}

// blockedItem is the answer a real cluster gives once a flood-stage disk
// watermark has flipped an index to read-only-allow-delete: HTTP 200 on the
// bulk, 429 on every document inside it.
var blockedItem = itemVerdict{
	status: 429,
	typ:    "cluster_block_exception",
	reason: "index [t_ref] blocked by: [TOO_MANY_REQUESTS/12/disk usage exceeded flood-stage watermark, index has read-only-allow-delete block];",
}

var rejectedItem = itemVerdict{
	status: 400,
	typ:    "mapper_parsing_exception",
	reason: "failed to parse field [url] of type [keyword]",
}

// fakeBulkES answers each successive bulk with the next verdict plan, and
// records the document ids each request carried.
type fakeBulkES struct {
	mu     sync.Mutex
	plans  [][]itemVerdict // one entry per expected bulk request
	calls  [][]string      // ids seen, per request
	action string
}

func (f *fakeBulkES) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `{"version":{"number":"8.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)
			return
		}

		ids := bulkIDs(r)

		f.mu.Lock()
		n := len(f.calls)
		f.calls = append(f.calls, ids)
		var plan []itemVerdict
		if n < len(f.plans) {
			plan = f.plans[n]
		}
		f.mu.Unlock()

		items := make([]string, 0, len(ids))
		for i := range ids {
			v := okItem
			if i < len(plan) {
				v = plan[i]
			}
			if v.status <= 201 {
				items = append(items, fmt.Sprintf(`{%q:{"_id":%q,"status":200,"result":"updated"}}`, f.action, ids[i]))
				continue
			}
			items = append(items, fmt.Sprintf(`{%q:{"_id":%q,"status":%d,"error":{"type":%q,"reason":%q}}}`,
				f.action, ids[i], v.status, v.typ, v.reason))
		}
		fmt.Fprintf(w, `{"errors":true,"items":[%s]}`, strings.Join(items, ","))
	})
}

// bulkIDs pulls the _id out of every action line of an NDJSON bulk body.
func bulkIDs(r *http.Request) []string {
	var ids []string
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]struct {
			ID string `json:"_id"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		for _, v := range m {
			if v.ID != "" {
				ids = append(ids, v.ID)
			}
		}
	}
	return ids
}

func newTestWriter(t *testing.T, f *fakeBulkES) (*ElasticWriter, func()) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	// The client's own retry layer would blur the request counts these tests
	// assert on; what is under test is the writer's handling.
	c, err := elk.NewClient(elk.Config{Addresses: []string{srv.URL}, DisableRetry: true})
	if err != nil {
		srv.Close()
		t.Fatalf("client: %v", err)
	}
	return &ElasticWriter{
		Client:      c,
		Index:       "t",
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}, srv.Close
}

func docs(ids ...string) map[string][]byte {
	out := map[string][]byte{}
	for _, id := range ids {
		out[id] = []byte(fmt.Sprintf(`{"v":%q}`, id))
	}
	return out
}

func shortPause(t *testing.T) {
	t.Helper()
	orig := elkPauseInterval
	elkPauseInterval = 5 * time.Millisecond
	t.Cleanup(func() { elkPauseInterval = orig })
}

// A bulk answering 200 with every item blocked is NOT a success: the documents
// have to be held and re-sent, not counted and dropped.
func TestPostBulkRetriesBlockedItems(t *testing.T) {
	shortPause(t)
	f := &fakeBulkES{action: "index", plans: [][]itemVerdict{
		{blockedItem, blockedItem, blockedItem},
		{blockedItem, blockedItem, blockedItem},
		{okItem, okItem, okItem},
	}}
	ew, done := newTestWriter(t, f)
	defer done()

	if err := ew.sendBulk("t_ref", "index", docs("a", "b", "c")); err != nil {
		t.Fatalf("sendBulk: %v", err)
	}

	if len(f.calls) != 3 {
		t.Fatalf("made %d bulk requests, want 3", len(f.calls))
	}
	for i, ids := range f.calls {
		if len(ids) != 3 {
			t.Errorf("request %d carried %d documents, want all 3 held", i, len(ids))
		}
	}
	if got := ew.metDocs.Load(); got != 3 {
		t.Errorf("recorded %d written documents, want 3", got)
	}
	if got := ew.metDocErrs.Load(); got != 0 {
		t.Errorf("recorded %d dropped documents, want 0", got)
	}
	if ew.gate.total.Load() == 0 {
		t.Error("the pause was never accounted for in the metrics")
	}
	if ew.gate.pausedFor() != 0 {
		t.Error("the gate was left engaged after the cluster recovered")
	}
}

// Only the documents that actually failed get re-sent.
func TestPostBulkRetriesOnlyFailedItems(t *testing.T) {
	shortPause(t)
	f := &fakeBulkES{action: "index", plans: [][]itemVerdict{
		{okItem, blockedItem, okItem, blockedItem},
	}}
	ew, done := newTestWriter(t, f)
	defer done()

	if err := ew.sendBulk("t_ref", "index", docs("a", "b", "c", "d")); err != nil {
		t.Fatalf("sendBulk: %v", err)
	}

	if len(f.calls) != 2 {
		t.Fatalf("made %d bulk requests, want 2", len(f.calls))
	}
	// The map iteration order decides which ids land in slots 1 and 3, so the
	// assertion is on the retry being exactly the two the fake refused.
	refused := map[string]bool{f.calls[0][1]: true, f.calls[0][3]: true}
	if len(f.calls[1]) != 2 {
		t.Fatalf("retry carried %d documents, want 2", len(f.calls[1]))
	}
	for _, id := range f.calls[1] {
		if !refused[id] {
			t.Errorf("retry re-sent %q, which had already been written", id)
		}
	}
	if got := ew.metDocs.Load(); got != 4 {
		t.Errorf("recorded %d written documents, want 4", got)
	}
}

// A document the cluster will never accept must be dropped and counted, not
// retried forever -- one bad document cannot be allowed to stall the run.
func TestPostBulkDropsPermanentFailures(t *testing.T) {
	shortPause(t)
	f := &fakeBulkES{action: "index", plans: [][]itemVerdict{
		{okItem, rejectedItem, okItem},
	}}
	ew, done := newTestWriter(t, f)
	defer done()

	if err := ew.sendBulk("t_ref", "index", docs("a", "b", "c")); err != nil {
		t.Fatalf("sendBulk: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("made %d bulk requests, want 1 (a mapping error is not retriable)", len(f.calls))
	}
	if got := ew.metDocErrs.Load(); got != 1 {
		t.Errorf("recorded %d dropped documents, want 1", got)
	}
	if got := ew.metDocs.Load(); got != 2 {
		t.Errorf("recorded %d written documents, want 2", got)
	}
}

// While one goroutine is probing a blocked cluster the others must park on the
// gate instead of piling more requests onto it.
func TestClusterGateParksOtherWriters(t *testing.T) {
	var g clusterGate

	if !g.pause() {
		t.Fatal("the first caller should own the pause")
	}
	if g.pause() {
		t.Fatal("a second caller must not also own the pause")
	}

	released := make(chan struct{})
	go func() {
		g.wait()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("wait() returned while the gate was engaged")
	case <-time.After(20 * time.Millisecond):
	}

	if d := g.resume(); d <= 0 {
		t.Errorf("resume reported %s of pause, want a positive duration", d)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("wait() did not return after the gate was released")
	}
	if g.pausedFor() != 0 {
		t.Error("gate still reports itself paused after resume")
	}
}

// The leak upsert path renders the same lines the retry mechanism re-sends, so
// a blocked leak bulk must survive the same way.
func TestSendLeakBulkRetriesBlockedItems(t *testing.T) {
	shortPause(t)
	f := &fakeBulkES{action: "update", plans: [][]itemVerdict{
		{blockedItem, blockedItem},
	}}
	ew, done := newTestWriter(t, f)
	defer done()

	pending := map[string]*pendingLeak{
		"h1": {doc: []byte(`{"url":"a"}`), first: "2024-01-01T00:00:00Z", last: "2024-01-01T00:00:00Z"},
		"h2": {doc: []byte(`{"url":"b"}`), first: "2024-01-01T00:00:00Z", last: "2025-01-01T00:00:00Z"},
	}
	if err := ew.sendLeakBulk("t_urls", pending); err != nil {
		t.Fatalf("sendLeakBulk: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("made %d bulk requests, want 2", len(f.calls))
	}
	if got := ew.metDocs.Load(); got != 2 {
		t.Errorf("recorded %d written documents, want 2", got)
	}
	// The retry must carry the update envelope, not a bare index action.
	if !bytes.Contains([]byte(strings.Join(f.calls[1], ",")), []byte("h")) {
		t.Error("retry did not carry the leak ids")
	}
}

// scriptedES answers each bulk with the next canned (status, body) pair.
type scriptedES struct {
	mu    sync.Mutex
	steps []struct {
		status int
		body   string
	}
	calls [][]string
}

func (f *scriptedES) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `{"version":{"number":"8.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)
			return
		}
		ids := bulkIDs(r)

		f.mu.Lock()
		n := len(f.calls)
		f.calls = append(f.calls, ids)
		step := f.steps[len(f.steps)-1]
		if n < len(f.steps) {
			step = f.steps[n]
		}
		f.mu.Unlock()

		body := step.body
		if body == "" {
			items := make([]string, 0, len(ids))
			for _, id := range ids {
				items = append(items, fmt.Sprintf(`{"index":{"_id":%q,"status":200,"result":"created"}}`, id))
			}
			body = fmt.Sprintf(`{"errors":false,"items":[%s]}`, strings.Join(items, ","))
		}
		w.WriteHeader(step.status)
		fmt.Fprint(w, body)
	})
}

func newScriptedWriter(t *testing.T, f *scriptedES) (*ElasticWriter, func()) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	c, err := elk.NewClient(elk.Config{Addresses: []string{srv.URL}, DisableRetry: true})
	if err != nil {
		srv.Close()
		t.Fatalf("client: %v", err)
	}
	return &ElasticWriter{
		Client:      c,
		Index:       "t",
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}, srv.Close
}

// A whole bulk refused at the HTTP level (503, and the client out of its own
// retries) must be held and re-sent, not dropped.
func TestPostBulkRetriesWholeBulkRefusal(t *testing.T) {
	shortPause(t)
	f := &scriptedES{steps: []struct {
		status int
		body   string
	}{
		{status: 503, body: `{"error":{"type":"unavailable_shards_exception","reason":"primary shard is not active"}}`},
		{status: 503, body: `{"error":{"type":"unavailable_shards_exception","reason":"primary shard is not active"}}`},
		{status: 200},
	}}
	ew, done := newScriptedWriter(t, f)
	defer done()

	if err := ew.sendBulk("t_ref", "index", docs("a", "b")); err != nil {
		t.Fatalf("sendBulk: %v", err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("made %d bulk requests, want 3", len(f.calls))
	}
	if got := ew.metDocs.Load(); got != 2 {
		t.Errorf("recorded %d written documents, want 2", got)
	}
	if ew.gate.total.Load() == 0 {
		t.Error("a refused bulk did not register as a pause")
	}
}

// A 200 whose items do not account for every document sent says nothing
// reliable about what landed, so the batch must be re-sent rather than assumed
// written. Re-sending is safe: every id this writer produces is deterministic.
func TestPostBulkResendsOnTruncatedResponse(t *testing.T) {
	f := &scriptedES{steps: []struct {
		status int
		body   string
	}{
		{status: 200, body: `{"errors":false,"items":[{"index":{"_id":"a","status":200}}]}`},
		{status: 200},
	}}
	ew, done := newScriptedWriter(t, f)
	defer done()

	if err := ew.sendBulk("t_ref", "index", docs("a", "b", "c")); err != nil {
		t.Fatalf("sendBulk: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("made %d bulk requests, want 2", len(f.calls))
	}
	if len(f.calls[1]) != 3 {
		t.Errorf("re-sent %d documents, want all 3", len(f.calls[1]))
	}
	if got := ew.metDocs.Load(); got != 3 {
		t.Errorf("recorded %d written documents, want 3", got)
	}
}
