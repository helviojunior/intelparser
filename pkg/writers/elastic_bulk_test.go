package writers

import (
	"encoding/json"
	"fmt"
	"strings"
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

// The whole point of ELK_BULK_BYTES is that it bounds what goes on the wire, so
// what queueLeak/queueDoc charge against the budget has to be exactly what
// renderBulk later produces. It used to be a guess (220 bytes per leak against a
// real cost of 546), which is how a 5 MB budget turned into 9 MB requests.
func TestPendingBytesMatchRenderedPayload(t *testing.T) {
	ew := &ElasticWriter{
		pendUpdates: map[string]map[string]*pendingLeak{},
		pendIndexes: map[string]map[string][]byte{},
		pendBytes:   map[string]int{},
	}

	const ts = "2026-06-01T00:00:00Z"
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("hash-%d", i)
		if err := ew.queueLeak("idx_creds", id, []byte(`{"username":"u","password":"p"}`), ts); err != nil {
			t.Fatal(err)
		}
		if err := ew.queueDoc("idx_ref", "ref-"+id, []byte(`{"file_id":"f","leak_id":"l"}`)); err != nil {
			t.Fatal(err)
		}
	}

	leakLines := make([]bulkLine, 0)
	for id, p := range ew.pendUpdates["idx_creds"] {
		leakLines = append(leakLines, bulkLine{ndjson: append(append([]byte(bulkUpdateMeta(id)), leakUpsertLine(p)...), '\n')})
	}
	if got, want := len(renderBulk(leakLines)), ew.pendBytes["idx_creds"]; got != want {
		t.Errorf("leak payload = %d bytes, budget charged %d", got, want)
	}

	docLines := make([]bulkLine, 0)
	for id, doc := range ew.pendIndexes["idx_ref"] {
		docLines = append(docLines, bulkLine{ndjson: append(append([]byte(bulkDocMeta("index", id)), doc...), '\n')})
	}
	if got, want := len(renderBulk(docLines)), ew.pendBytes["idx_ref"]; got != want {
		t.Errorf("doc payload = %d bytes, budget charged %d", got, want)
	}

	if ew.pendTotal != ew.pendBytes["idx_creds"]+ew.pendBytes["idx_ref"] {
		t.Errorf("pendTotal = %d, want the sum of the per-index budgets", ew.pendTotal)
	}
}

// A file's _ctrl document carries the file's whole content, so it can be larger
// than the entire bulk budget on its own. It cannot be split, but it must not
// drag a full buffer onto the wire with it.
func TestQueueDocSendsOversizedDocumentAlone(t *testing.T) {
	f := &fakeBulkES{action: "index"}
	ew, closeFn := newTestWriter(t, f)
	defer closeFn()

	// A small document first: it must stay buffered.
	if err := ew.queueDoc("idx_ctrl", "small", []byte(`{"content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	big := fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", elkBulkMaxSize+1))
	if err := ew.queueDoc("idx_ctrl", "big", []byte(big)); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 1 {
		t.Fatalf("bulk requests = %d, want 1 (the oversized document, alone)", len(f.calls))
	}
	if got := f.calls[0]; len(got) != 1 || got[0] != "big" {
		t.Errorf("oversized bulk carried %v, want just [big]", got)
	}
	if _, ok := ew.pendIndexes["idx_ctrl"]["small"]; !ok {
		t.Error("the small document should still be buffered")
	}
}

// A superseded copy of an oversized document must not be left in the buffer to
// be flushed over the version that was just written.
func TestQueueDocOversizedDropsPendingCopy(t *testing.T) {
	f := &fakeBulkES{action: "index"}
	ew, closeFn := newTestWriter(t, f)
	defer closeFn()

	if err := ew.queueDoc("idx_ctrl", "f1", []byte(`{"content":"stale"}`)); err != nil {
		t.Fatal(err)
	}
	big := fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", elkBulkMaxSize+1))
	if err := ew.queueDoc("idx_ctrl", "f1", []byte(big)); err != nil {
		t.Fatal(err)
	}

	if _, ok := ew.pendIndexes["idx_ctrl"]["f1"]; ok {
		t.Error("the superseded copy is still buffered and would overwrite the document just written")
	}
	if got := ew.pendBytes["idx_ctrl"]; got != 0 {
		t.Errorf("pendBytes = %d after dropping the only buffered document, want 0", got)
	}
	if ew.pendTotal != 0 {
		t.Errorf("pendTotal = %d, want 0", ew.pendTotal)
	}
}
