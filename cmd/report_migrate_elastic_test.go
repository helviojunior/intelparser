package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	fmt.Fprintf(w, `{"_scroll_id":"s1","hits":{"hits":[%s]}}`, strings.Join(hits, ","))
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
	body := `{"query":{"match_all":{}},"stored_fields":[],"_source":true,"sort":["_doc"]}`
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
