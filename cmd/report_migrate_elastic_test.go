package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	elk "github.com/elastic/go-elasticsearch/v8"
)

// fakeES serves just enough of the search API to exercise countDocs/scrollAll:
// a _count, a first _search page and then scroll pages until exhausted.
type fakeES struct {
	total     int
	perPage   []int // sizes seen on the initial _search
	delivered int
	bodies    []string
}

func (f *fakeES) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/":
			fmt.Fprint(w, `{"version":{"number":"8.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)

		case strings.HasSuffix(r.URL.Path, "/_count"):
			fmt.Fprintf(w, `{"count":%d}`, f.total)

		case strings.HasSuffix(r.URL.Path, "/_search"):
			raw, _ := io.ReadAll(r.Body)
			f.bodies = append(f.bodies, string(raw))
			size := 0
			fmt.Sscanf(r.URL.Query().Get("size"), "%d", &size)
			f.perPage = append(f.perPage, size)
			// A fresh search restarts the result set; only scrolling continues it.
			f.delivered = 0
			f.writePage(w, size)

		case r.URL.Path == "/_search/scroll":
			f.writePage(w, f.perPage[0])

		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func (f *fakeES) writePage(w http.ResponseWriter, size int) {
	n := f.total - f.delivered
	if n > size {
		n = size
	}
	hits := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hits = append(hits, fmt.Sprintf(`{"_id":"id-%d","_source":{"file_name":"f-%d"}}`, f.delivered+i, f.delivered+i))
	}
	f.delivered += n
	fmt.Fprintf(w, `{"_scroll_id":"s1","hits":{"total":{"value":%d},"hits":[%s]}}`,
		f.total, strings.Join(hits, ","))
}

func newFakeClient(t *testing.T, f *fakeES) (*elk.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	c, err := elk.NewClient(elk.Config{Addresses: []string{srv.URL}})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c, srv.Close
}

func TestCountDocs(t *testing.T) {
	f := &fakeES{total: 1234}
	c, done := newFakeClient(t, f)
	defer done()

	got, err := countDocs(c, "src")
	if err != nil {
		t.Fatalf("countDocs: %v", err)
	}
	if got != 1234 {
		t.Errorf("count = %d, want 1234", got)
	}
}

// scrollAll must page at exactly the requested size and deliver every hit once.
func TestScrollAllPagesAtLimit(t *testing.T) {
	const total, limit = 1250, 500
	f := &fakeES{total: total}
	c, done := newFakeClient(t, f)
	defer done()

	seen := map[string]bool{}
	body := `{"query":{"match_all":{}},"stored_fields":[],"_source":true,"sort":["_doc"],"track_total_hits":true}`
	err := scrollAll(c, "src", body, limit, func(id string, src json.RawMessage) error {
		if seen[id] {
			t.Errorf("hit %s delivered twice", id)
		}
		seen[id] = true
		return nil
	})
	if err != nil {
		t.Fatalf("scrollAll: %v", err)
	}
	if len(seen) != total {
		t.Errorf("delivered %d hits, want %d", len(seen), total)
	}
	if f.perPage[0] != limit {
		t.Errorf("requested size = %d, want %d", f.perPage[0], limit)
	}
	if !strings.Contains(f.bodies[0], `"sort":["_doc"]`) {
		t.Errorf("body did not carry the _doc sort: %s", f.bodies[0])
	}
}

// A missing index must read as empty rather than as an error.
func TestScrollAllMissingIndexIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `{"version":{"number":"8.0.0","build_flavor":"default"},"tagline":"You Know, for Search"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"type":"index_not_found_exception"}}`)
	}))
	defer srv.Close()
	c, err := elk.NewClient(elk.Config{Addresses: []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := scrollAll(c, "gone", `{"query":{"match_all":{}}}`, 500, func(string, json.RawMessage) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("missing index should be empty, got: %v", err)
	}
	if n != 0 {
		t.Errorf("delivered %d hits from a missing index", n)
	}
}

// A result set that fits in one page must cost exactly one request: no scroll
// context, no trailing empty page, no clear.
func TestSearchAllSinglePage(t *testing.T) {
	f := &fakeES{total: 300}
	c, done := newFakeClient(t, f)
	defer done()

	n := 0
	body := `{"query":{"match_all":{}},"sort":["_doc"],"track_total_hits":true}`
	if err := searchAll(c, "src", body, 500, func(string, json.RawMessage) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("searchAll: %v", err)
	}
	if n != 300 {
		t.Errorf("delivered %d hits, want 300", n)
	}
	if len(f.perPage) != 1 {
		t.Errorf("issued %d searches, want 1", len(f.perPage))
	}
}

// A result set that overflows the page but still fits in the result window must
// be re-fetched sized to the total rather than scrolled.
func TestSearchAllRefetchesSizedToTotal(t *testing.T) {
	f := &fakeES{total: 1250}
	c, done := newFakeClient(t, f)
	defer done()

	seen := map[string]bool{}
	body := `{"query":{"match_all":{}},"sort":["_doc"],"track_total_hits":true}`
	if err := searchAll(c, "src", body, 500, func(id string, _ json.RawMessage) error {
		if seen[id] {
			t.Errorf("hit %s delivered twice", id)
		}
		seen[id] = true
		return nil
	}); err != nil {
		t.Fatalf("searchAll: %v", err)
	}
	if len(seen) != 1250 {
		t.Errorf("delivered %d hits, want 1250", len(seen))
	}
	if len(f.perPage) != 2 || f.perPage[1] != 1250 {
		t.Errorf("searches sized %v, want [500 1250]", f.perPage)
	}
}

// The progress line is the run's only sense of scale, so both of its modes have
// to hold: leak-based when the source counts are known, files-only when not.
func TestProgressLine(t *testing.T) {
	// 1% of the leaks in 6 minutes projects to just under ten more hours.
	got := progressLine(607, 33981, 12_400_000, 1_230_000_000, 6*time.Minute)
	for _, want := range []string{"607/33.981 files", "12.400.000/1.230.000.000 leaks", "(1.0%)", "leaks/s", "ETA 9h"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line missing %q:\n%s", want, got)
		}
	}

	// Without source counts there is nothing to project from.
	if got := progressLine(607, 33981, 0, 0, 6*time.Minute); got != "Queued 607/33.981 files for migration" {
		t.Errorf("files-only line = %q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:               "<1m",
		9*time.Minute + 40*time.Second: "10m",
		9*time.Hour + 32*time.Minute:   "9h32m",
		25*time.Hour + 5*time.Minute:   "25h05m",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
