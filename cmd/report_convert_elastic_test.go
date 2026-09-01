package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/helviojunior/intelparser/pkg/models"
)

// withFilter swaps the package-level filter list for the duration of a test.
func withFilter(t *testing.T, terms ...string) {
	t.Helper()
	old := filterList
	filterList = terms
	t.Cleanup(func() { filterList = old })
}

// mustJSON fails the test unless body is a well-formed JSON object. Every query
// here is assembled by hand, so a stray comma is a realistic mistake and one the
// cluster would only report at runtime.
func mustJSON(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("query is not valid JSON: %s\n%s", err, body)
	}
	return out
}

func TestFileListBodyCarriesBothDateFilters(t *testing.T) {
	indexed := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	leak := time.Date(2023, 12, 25, 0, 0, 0, 0, time.UTC)

	oldIndexed, oldDate := opts.IndexedDateFilter, opts.DateFilter
	opts.IndexedDateFilter, opts.DateFilter = &indexed, &leak
	t.Cleanup(func() { opts.IndexedDateFilter, opts.DateFilter = oldIndexed, oldDate })

	body := elkFileListBody()
	mustJSON(t, body)

	if !strings.Contains(body, `"indexed_at":{"gte":"2024-03-01"}`) {
		t.Errorf("--indexed-date-from was not pushed into the query: %s", body)
	}
	if !strings.Contains(body, `"date":{"gte":"2023-12-25"}`) {
		t.Errorf("--date-from was not pushed into the query: %s", body)
	}
	if !strings.Contains(body, `"excludes":["content"]`) {
		t.Errorf("the listing should not carry file contents: %s", body)
	}
}

func TestFileListBodyWithoutFiltersMatchesAll(t *testing.T) {
	oldIndexed, oldDate := opts.IndexedDateFilter, opts.DateFilter
	opts.IndexedDateFilter, opts.DateFilter = nil, nil
	t.Cleanup(func() { opts.IndexedDateFilter, opts.DateFilter = oldIndexed, oldDate })

	body := elkFileListBody()
	mustJSON(t, body)

	if !strings.Contains(body, `"query":{"match_all":{}}`) {
		t.Errorf("an unfiltered run should list every file: %s", body)
	}
}

func TestLeakQueryPushesFilterTerms(t *testing.T) {
	withFilter(t, "acme")

	body := elkLeakQueryBody([]string{"a", "b"}, "_creds", nil)
	mustJSON(t, body)

	for _, field := range []string{"username", "url", "password"} {
		want := `{"wildcard":{"` + field + `":{"value":"*acme*","case_insensitive":true}}}`
		if !strings.Contains(body, want) {
			t.Errorf("missing wildcard on %s: %s", field, body)
		}
	}
	if !strings.Contains(body, `"minimum_should_match":1`) {
		t.Errorf("the terms must be an OR, not an AND: %s", body)
	}
	if !strings.Contains(body, `{"ids":{"values":["a","b"]}}`) {
		t.Errorf("the batch ids must still bound the query: %s", body)
	}
}

// A leak kept for its occurrence context has to be OR'd back in by id: near_text
// lives on the reference, so no query against the leak index can find it.
func TestLeakQueryKeepsNearTextMatches(t *testing.T) {
	withFilter(t, "acme")

	keep := map[string]struct{}{"b": {}, "zzz": {}}
	body := elkLeakQueryBody([]string{"a", "b"}, "_phone", keep)
	mustJSON(t, body)

	if !strings.Contains(body, `{"ids":{"values":["b"]}}`) {
		t.Errorf("the near_text match was not preserved: %s", body)
	}
	if strings.Contains(body, `"zzz"`) {
		t.Errorf("only ids from this chunk belong in the query: %s", body)
	}
}

func TestLeakQueryWithoutFilterIsPlainIdsLookup(t *testing.T) {
	withFilter(t)

	body := elkLeakQueryBody([]string{"a"}, "_emails", nil)
	mustJSON(t, body)

	if strings.Contains(body, "wildcard") || strings.Contains(body, "should") {
		t.Errorf("an unfiltered run should not build a bool query: %s", body)
	}
}

// A --filter-file with thousands of terms would build a bool query past
// max_clause_count; the export has to fall back to filtering locally rather
// than sending something the cluster rejects.
func TestLeakQueryFallsBackWhenTooManyClauses(t *testing.T) {
	terms := make([]string, elkMaxFilterClauses)
	for i := range terms {
		terms[i] = "term"
	}
	withFilter(t, terms...)

	body := elkLeakQueryBody([]string{"a"}, "_creds", nil)
	mustJSON(t, body)

	if strings.Contains(body, "wildcard") {
		t.Errorf("too many terms should not be pushed down: %s", body)
	}
}

func TestEscapeWildcardNeutralisesOperators(t *testing.T) {
	if got := elkEscapeWildcard(`a*b?c\d`); got != `a\*b\?c\\d` {
		t.Errorf("elkEscapeWildcard = %q", got)
	}
}

// rebuildLeak has to put the two halves the writer split apart back together:
// the deduplicated value and the occurrence context of one reference.
func TestRebuildLeakMergesValueAndContext(t *testing.T) {
	leak := json.RawMessage(`{"country":"BR","raw":"11 91234-5678","phone":"5511912345678",
		"inserted_at":"2024-01-01T00:00:00Z","last_reference_at":"2024-02-01T00:00:00Z"}`)
	ref := map[string]json.RawMessage{
		"file_id":   json.RawMessage(`"deadbeef"`),
		"leak_id":   json.RawMessage(`"cafe"`),
		"type":      json.RawMessage(`"phone"`),
		"bucket":    json.RawMessage(`"leaks"`),
		"source":    json.RawMessage(`"dump.txt"`),
		"file_name": json.RawMessage(`"dump.txt"`),
		"line":      json.RawMessage(`"call 11 91234-5678"`),
		"near_text": json.RawMessage(`"contact"`),
	}

	var p models.Phone
	if err := rebuildLeak(leak, ref, &p); err != nil {
		t.Fatalf("rebuildLeak: %s", err)
	}

	if p.Phone != "5511912345678" || p.Country != "BR" || p.Raw != "11 91234-5678" {
		t.Errorf("leak value lost: %+v", p)
	}
	if p.NearText != "contact" || p.Line != "call 11 91234-5678" || p.Source != "dump.txt" {
		t.Errorf("occurrence context lost: %+v", p)
	}
	// The pointer fields address the leak, they do not describe it, and one of
	// them (file_id) is a fingerprint string that would not fit the model's
	// numeric FileID at all.
	if p.FileID != 0 || p.ID != 0 {
		t.Errorf("reference pointer fields leaked into the model: %+v", p)
	}
}

func TestLeakEnabledHonoursDisableFlags(t *testing.T) {
	oldUrl, oldEmail := rptDisableUrl, rptDisableEmail
	rptDisableUrl, rptDisableEmail = true, false
	t.Cleanup(func() { rptDisableUrl, rptDisableEmail = oldUrl, oldEmail })

	if elkLeakEnabled("url") {
		t.Error("--disable-url should drop url references")
	}
	if !elkLeakEnabled("email") || !elkLeakEnabled("credential") {
		t.Error("only the disabled type should be dropped")
	}
}
