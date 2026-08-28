package writers

import (
	"encoding/json"
	"testing"
)

// leakUpsertLine splices the two dates onto an already-marshalled document, so
// the result has to stay valid JSON and keep the intrinsic fields intact —
// including for the degenerate empty document.
func TestLeakUpsertLine(t *testing.T) {
	const first = "2024-01-02T03:04:05Z"
	const last = "2025-06-07T08:09:10Z"

	for name, doc := range map[string]string{
		"with fields": `{"email":"a@b.c","domain":"b.c"}`,
		"empty":       `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var v struct {
				Script struct {
					Lang   string `json:"lang"`
					Source string `json:"source"`
					Params struct {
						First string `json:"first"`
						Last  string `json:"last"`
					} `json:"params"`
				} `json:"script"`
				Upsert map[string]interface{} `json:"upsert"`
			}
			line := leakUpsertLine(&pendingLeak{doc: []byte(doc), first: first, last: last})
			if err := json.Unmarshal(line, &v); err != nil {
				t.Fatalf("invalid JSON: %v\n%s", err, line)
			}
			if v.Script.Lang != "painless" || v.Script.Source != elkLeakDateScript {
				t.Errorf("script = %q/%q", v.Script.Lang, v.Script.Source)
			}
			if v.Script.Params.First != first || v.Script.Params.Last != last {
				t.Errorf("params = %+v", v.Script.Params)
			}
			if v.Upsert["inserted_at"] != first || v.Upsert["last_reference_at"] != last {
				t.Errorf("upsert dates = %v / %v", v.Upsert["inserted_at"], v.Upsert["last_reference_at"])
			}

			var want map[string]interface{}
			if err := json.Unmarshal([]byte(doc), &want); err != nil {
				t.Fatal(err)
			}
			for k, w := range want {
				if v.Upsert[k] != w {
					t.Errorf("upsert[%q] = %v, want %v", k, v.Upsert[k], w)
				}
			}
		})
	}
}

// The bulk buffers now span files, so the same leak can be queued more than
// once before a flush. Coalescing must widen the date range rather than let the
// last writer win — that is what preserves the earliest inserted_at.
func TestQueueLeakCoalescesDateRange(t *testing.T) {
	ew := &ElasticWriter{
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}

	// Deliberately out of order: middle, latest, earliest.
	for _, ts := range []string{"2024-05-05T00:00:00Z", "2025-01-01T00:00:00Z", "2023-01-01T00:00:00Z"} {
		if err := ew.queueLeak("idx_creds", "hash1", []byte(`{"username":"u"}`), ts); err != nil {
			t.Fatalf("queueLeak(%s): %v", ts, err)
		}
	}

	m := ew.pendUpdates["idx_creds"]
	if len(m) != 1 {
		t.Fatalf("pending entries = %d, want 1 (the three occurrences should coalesce)", len(m))
	}
	p := m["hash1"]
	if p.first != "2023-01-01T00:00:00Z" {
		t.Errorf("first = %s, want the earliest timestamp", p.first)
	}
	if p.last != "2025-01-01T00:00:00Z" {
		t.Errorf("last = %s, want the latest timestamp", p.last)
	}
}

// A repeated document id must not be counted twice against the flush budget.
func TestQueueDocReplacesWithoutDoubleCounting(t *testing.T) {
	ew := &ElasticWriter{
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}

	if err := ew.queueDoc("idx_ref", "ref1", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	once := ew.pendBytes["idx_ref"]
	if err := ew.queueDoc("idx_ref", "ref1", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := ew.pendBytes["idx_ref"]; got != once {
		t.Errorf("pendBytes = %d after re-queueing the same id, want %d", got, once)
	}
	if got := len(ew.pendIndexes["idx_ref"]); got != 1 {
		t.Errorf("pending docs = %d, want 1", got)
	}
}
