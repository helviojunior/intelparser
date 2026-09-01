package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	elk "github.com/elastic/go-elasticsearch/v8"
	"github.com/helviojunior/intelparser/internal/ascii"
	"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/models"
	"github.com/helviojunior/intelparser/pkg/writers"
)

// This is the inverse of the ElasticWriter, and it has to undo the same split.
// The writer stores a file's metadata in <index>_ctrl keyed by the file
// fingerprint, every leak *value* once in the deduplicated <index>_creds /
// _urls / _emails / _phone / _document, and one document per occurrence in the
// monthly <index>_ref_YYYY-MM indices, which is what ties a file to a leak and
// carries the context (near_text, line, source) that does not belong on a
// shared leak. Rebuilding a models.File therefore means three hops: the file,
// its references, and the leaks those references point at.

// elkReadBatch is how many files one reference lookup covers. Bigger batches
// mean fewer round-trips but hold more references in memory at once; a file can
// carry millions of leaks, so this stays modest.
const elkReadBatch = 500

// elkScanSize is the page size used for the reference and leak scans.
const elkScanSize = 1000

// elkIdsChunk caps how many leak ids go into a single ids query, so a batch of
// files with a lot of distinct leaks does not build one enormous request.
const elkIdsChunk = 5000

// elkMaxFilterClauses bounds how many wildcard clauses --filter may push into a
// single query. Elasticsearch refuses a bool query past indices.query.bool.
// max_clause_count (1024 by default), so a long --filter-file falls back to
// filtering the results client-side instead of building a query the cluster
// would reject.
const elkMaxFilterClauses = 512

// elkLeakIndexes maps the reference discriminator (models.LeakIndexable.LeakType)
// to the index suffix holding the leak itself.
var elkLeakIndexes = map[string]string{
	"credential": "_creds",
	"url":        "_urls",
	"email":      "_emails",
	"phone":      "_phone",
	"document":   "_document",
}

// elkLeakFilterFields lists, per leak index, the fields --filter is matched
// against. They mirror what getFilteredOnly checks client-side, so pushing the
// terms down narrows what the cluster ships without changing which leaks
// survive. near_text is not here: it lives on the reference, not on the shared
// leak (see elkNearTextKeep).
var elkLeakFilterFields = map[string][]string{
	"_creds":    {"username", "url", "password"},
	"_urls":     {"url"},
	"_emails":   {"email"},
	"_phone":    {"phone", "raw"},
	"_document": {"number", "raw"},
}

// ctrlFileDoc is the file document as the current model stores it. Date is what
// the writer marshals from models.File; leak_date is accepted as well because
// that is the name the index mapping declares for it.
type ctrlFileDoc struct {
	Provider   string    `json:"provider"`
	FilePath   string    `json:"file_path"`
	FileName   string    `json:"file_name"`
	Name       string    `json:"name"`
	Date       time.Time `json:"date"`
	LeakDate   time.Time `json:"leak_date"`
	Bucket     string    `json:"bucket"`
	MediaType  string    `json:"media_type"`
	IndexedAt  time.Time `json:"indexed_at"`
	Size       uint      `json:"size"`
	ProviderId string    `json:"provider_id"`
	MIMEType   string    `json:"mime_type"`
}

// date is the file's leak date, whichever field carried it.
func (d *ctrlFileDoc) date() time.Time {
	if !d.Date.IsZero() {
		return d.Date
	}
	return d.LeakDate
}

// elkFile is one entry of the file listing: the document plus its _id, which is
// the fingerprint every reference points at.
type elkFile struct {
	id  string
	doc *ctrlFileDoc
}

// elkRef is one file<->leak reference. src is the whole reference document, so
// the occurrence context can be merged back into the leak without naming each
// field of each type here.
type elkRef struct {
	leakID string
	typ    string
	src    map[string]json.RawMessage
}

// elkLeakEnabled reports whether a reference type should be replayed at all.
// --disable-url / --disable-email drop the type before its leaks are ever
// fetched, so a suppressed index costs nothing to read.
func elkLeakEnabled(typ string) bool {
	switch typ {
	case "url":
		return !rptDisableUrl
	case "email":
		return !rptDisableEmail
	}
	return true
}

// convertFromElasticTo replays an Elasticsearch index into writer, one batch of
// files at a time. The file listing is streamed rather than collected: only the
// batch being rebuilt is held, so the memory it needs does not grow with the
// size of the dataset.
func convertFromElasticTo(uri string, writer writers.Writer, status *ConvStatus) error {
	defer clearScreen()
	ascii.HideCursor()

	log.Info("starting conversion...")

	client, index, err := writers.NewElasticClient(uri, false)
	if err != nil {
		return err
	}

	ctrlIndex := index + "_ctrl"
	// One wildcard covers every monthly partition; a reference is looked up by
	// file_id, and which month it landed in is not known in advance.
	refIndex := index + "_ref_*"

	total, err := countDocs(client, ctrlIndex)
	if err != nil {
		return fmt.Errorf("counting source file index %q: %w", ctrlIndex, err)
	}
	if total == 0 {
		return fmt.Errorf("no documents found in source file index %q", ctrlIndex)
	}
	log.Infof("Source index %q holds %s file documents", ctrlIndex, tools.FormatInt64Comma(total))

	batch := make([]elkFile, 0, elkReadBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := convertElasticBatch(client, index, ctrlIndex, refIndex, batch, writer, status)
		batch = batch[:0]
		return err
	}

	err = searchAll(client, ctrlIndex, elkFileListBody(), elkReadBatch, func(id string, src json.RawMessage) error {
		var doc ctrlFileDoc
		if err := json.Unmarshal(src, &doc); err != nil {
			return err
		}

		// --date-from filters the leak date. The current model keeps no date on
		// the leak itself (the leak indices hold first/last sighting of a value
		// shared by many files), so the file's own date is what it applies to.
		if opts.DateFilter != nil && !doc.date().IsZero() && doc.date().Before(*opts.DateFilter) {
			return nil
		}

		batch = append(batch, elkFile{id: id, doc: &doc})
		if len(batch) < elkReadBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return fmt.Errorf("reading source file index %q: %w", ctrlIndex, err)
	}

	return flush()
}

// elkFileListBody lists the file documents in _doc order -- Lucene's own order,
// which skips scoring entirely. content is excluded: it is the biggest field by
// far and is fetched per file while the file is being rebuilt.
//
// Both date filters are pushed into the query: a file the cluster can rule out
// costs nothing here, while one shipped and dropped locally costs a document
// plus the reference and leak lookups the batch it lands in would run for it.
func elkFileListBody() string {
	filters := []string{}
	if opts.IndexedDateFilter != nil {
		filters = append(filters, fmt.Sprintf(`{"range":{"indexed_at":{"gte":%s}}}`,
			jsonString(opts.IndexedDateFilter.Format("2006-01-02"))))
	}
	if opts.DateFilter != nil {
		filters = append(filters, fmt.Sprintf(`{"range":{"date":{"gte":%s}}}`,
			jsonString(opts.DateFilter.Format("2006-01-02"))))
	}

	query := `{"match_all":{}}`
	if len(filters) > 0 {
		query = fmt.Sprintf(`{"bool":{"filter":[%s]}}`, strings.Join(filters, ","))
	}
	return fmt.Sprintf(`{"query":%s,"_source":{"excludes":["content"]},"sort":["_doc"],"track_total_hits":true}`, query)
}

// elkNearTextFiltered reports whether the client-side filter for a leak type
// also looks at near_text. For those types a leak can survive --filter on the
// strength of its occurrence context alone, which lives on the reference and is
// therefore invisible to a query against the leak index.
func elkNearTextFiltered(typ string) bool {
	return typ == "phone" || typ == "document"
}

// elkLeakQueryBody is the leak lookup for one chunk of ids. --filter is pushed
// into it as a wildcard per field per term: the leak indices are the largest
// ones by far, and a filtered export would otherwise pull every leak of every
// listed file across the wire only to drop most of them locally.
//
// The ids of leaks whose reference near_text already matched are OR'd back in,
// so pushing the terms down cannot lose a leak the client-side filter would
// have kept. Nothing is pushed down at all when the terms would build more
// clauses than a bool query accepts.
func elkLeakQueryBody(ids []string, suffix string, keep map[string]struct{}) string {
	idsClause := fmt.Sprintf(`{"ids":{"values":%s}}`, mustMarshal(ids))

	fields := elkLeakFilterFields[suffix]
	if len(filterList) == 0 || len(fields) == 0 ||
		len(fields)*len(filterList) > elkMaxFilterClauses {
		return fmt.Sprintf(`{"query":%s,"_source":true,"sort":["_doc"],"track_total_hits":true}`, idsClause)
	}

	should := make([]string, 0, len(fields)*len(filterList)+1)
	for _, field := range fields {
		for _, term := range filterList {
			// case_insensitive because --filter is matched case-insensitively
			// client-side, and the fields it looks at are keywords stored with
			// their original casing.
			should = append(should, fmt.Sprintf(`{"wildcard":{%s:{"value":%s,"case_insensitive":true}}}`,
				jsonString(field), jsonString("*"+elkEscapeWildcard(term)+"*")))
		}
	}

	if kept := elkKeptIDs(ids, keep); len(kept) > 0 {
		should = append(should, fmt.Sprintf(`{"ids":{"values":%s}}`, mustMarshal(kept)))
	}

	return fmt.Sprintf(`{"query":{"bool":{"filter":%s,"should":[%s],"minimum_should_match":1}},"_source":true,"sort":["_doc"],"track_total_hits":true}`,
		idsClause, strings.Join(should, ","))
}

// elkKeptIDs is the subset of ids that must be returned regardless of the
// wildcard terms.
func elkKeptIDs(ids []string, keep map[string]struct{}) []string {
	if len(keep) == 0 {
		return nil
	}
	out := make([]string, 0, len(keep))
	for _, id := range ids {
		if _, ok := keep[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// elkEscapeWildcard neutralises the two characters a wildcard query treats as
// operators, so a filter term is matched literally.
func elkEscapeWildcard(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, "*", `\*`)
	return strings.ReplaceAll(term, "?", `\?`)
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// convertElasticBatch resolves the references and leaks of one batch of files
// and writes the rebuilt records.
func convertElasticBatch(client *elk.Client, index, ctrlIndex, refIndex string,
	batch []elkFile, writer writers.Writer, status *ConvStatus) error {

	ids := make([]string, len(batch))
	for i, f := range batch {
		ids[i] = f.id
	}

	refs, err := fetchFileRefs(client, refIndex, ids)
	if err != nil {
		return err
	}

	// Resolve the leak values per index rather than per reference: the same
	// leak is deliberately shared by every file it was seen in, so a batch of
	// hundreds of files usually needs far fewer leak documents than it holds
	// references.
	wanted := map[string]map[string]struct{}{}
	// Leaks that --filter would keep for their occurrence context rather than
	// their value: the near_text sits on the reference, so it is matched here
	// and the ids are handed to the leak query, which cannot see it.
	keep := map[string]map[string]struct{}{}
	add := func(m map[string]map[string]struct{}, suffix, id string) {
		set := m[suffix]
		if set == nil {
			set = map[string]struct{}{}
			m[suffix] = set
		}
		set[id] = struct{}{}
	}

	for _, list := range refs {
		for _, r := range list {
			suffix, ok := elkLeakIndexes[r.typ]
			if !ok || !elkLeakEnabled(r.typ) {
				continue
			}
			add(wanted, suffix, r.leakID)

			if elkNearTextFiltered(r.typ) && len(filterList) > 0 {
				nearText, err := refString(r.src, "near_text")
				if err != nil {
					return err
				}
				if containsFilterWord(nearText) {
					add(keep, suffix, r.leakID)
				}
			}
		}
	}

	leaks := make(map[string]map[string]json.RawMessage, len(wanted))
	for suffix, set := range wanted {
		docs, err := fetchLeakDocs(client, index+suffix, suffix, set, keep[suffix])
		if err != nil {
			return err
		}
		leaks[suffix] = docs
	}

	for _, f := range batch {
		file, err := buildElasticFile(client, ctrlIndex, f, refs[f.id], leaks)
		if err != nil {
			// One unreadable file should not throw away an export of millions:
			// report it and carry on with the rest of the dataset.
			log.Errorf("Skipping file %s (%s): %s", f.id, f.doc.FileName, err)
			continue
		}

		filtered := getFilteredOnly(*file)
		if filtered == nil {
			continue
		}

		if err := writer.Write(filtered); err != nil {
			return err
		}
		status.Converted++
		status.Credential += len(filtered.Credentials)
		status.Url += len(filtered.URLs)
		status.Email += len(filtered.Emails)
	}

	return nil
}

// fetchFileRefs resolves every reference belonging to a batch of files with one
// terms query over the monthly reference indices, and returns them by file id.
func fetchFileRefs(client *elk.Client, refIndex string, ids []string) (map[string][]elkRef, error) {
	out := map[string][]elkRef{}
	err := searchAll(client, refIndex, termsBody(ids), elkScanSize, func(_ string, src json.RawMessage) error {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(src, &m); err != nil {
			return err
		}

		fileID, err := refString(m, "file_id")
		if err != nil {
			return err
		}
		leakID, err := refString(m, "leak_id")
		if err != nil {
			return err
		}
		typ, err := refString(m, "type")
		if err != nil {
			return err
		}
		if fileID == "" || leakID == "" {
			return nil
		}

		out[fileID] = append(out[fileID], elkRef{leakID: leakID, typ: typ, src: m})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading references from %q: %w", refIndex, err)
	}
	return out, nil
}

// refString reads one string field of a reference document, treating an absent
// field as empty rather than as an error.
func refString(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// fetchLeakDocs fetches the leak documents with the given ids from one leak
// index, in chunks, and returns their sources keyed by id. A leak the query
// filters out is simply absent from the result, which is how --filter drops it.
func fetchLeakDocs(client *elk.Client, index, suffix string,
	ids map[string]struct{}, keep map[string]struct{}) (map[string]json.RawMessage, error) {

	out := make(map[string]json.RawMessage, len(ids))

	chunk := make([]string, 0, elkIdsChunk)
	fetch := func() error {
		if len(chunk) == 0 {
			return nil
		}
		err := searchAll(client, index, elkLeakQueryBody(chunk, suffix, keep), len(chunk),
			func(id string, src json.RawMessage) error {
				out[id] = src
				return nil
			})
		chunk = chunk[:0]
		return err
	}

	for id := range ids {
		chunk = append(chunk, id)
		if len(chunk) < elkIdsChunk {
			continue
		}
		if err := fetch(); err != nil {
			return nil, fmt.Errorf("reading leaks from %q: %w", index, err)
		}
	}
	if err := fetch(); err != nil {
		return nil, fmt.Errorf("reading leaks from %q: %w", index, err)
	}

	return out, nil
}

// buildElasticFile reassembles one models.File from its metadata document, its
// references, and the leak values already resolved for the batch. content is
// the one thing still fetched per file: holding the bodies of a whole batch in
// memory is exactly what the batching avoids.
func buildElasticFile(client *elk.Client, ctrlIndex string, f elkFile,
	refs []elkRef, leaks map[string]map[string]json.RawMessage) (*models.File, error) {

	content, err := getFileContent(client, ctrlIndex, f.id)
	if err != nil {
		return nil, err
	}

	d := f.doc
	date := d.date()
	file := &models.File{
		Provider:    d.Provider,
		FilePath:    d.FilePath,
		FileName:    d.FileName,
		Name:        d.Name,
		Date:        date,
		Bucket:      d.Bucket,
		MediaType:   d.MediaType,
		IndexedAt:   d.IndexedAt,
		Size:        d.Size,
		ProviderId:  d.ProviderId,
		MIMEType:    d.MIMEType,
		Fingerprint: f.id,
		Content:     content,
	}

	for _, r := range refs {
		suffix, ok := elkLeakIndexes[r.typ]
		if !ok || !elkLeakEnabled(r.typ) {
			continue
		}

		// A reference whose leak is gone from the leak index carries only the
		// occurrence context -- near_text without the value it sat next to --
		// so there is nothing to rebuild from it.
		src, ok := leaks[suffix][r.leakID]
		if !ok {
			log.Debugf("Leak %s referenced by file %s is missing from %s", r.leakID, f.id, suffix)
			continue
		}

		switch r.typ {
		case "credential":
			var c models.Credential
			if err := rebuildLeak(src, r.src, &c); err != nil {
				return nil, err
			}
			c.Time = date
			file.Credentials = append(file.Credentials, c)
		case "url":
			var u models.URL
			if err := rebuildLeak(src, r.src, &u); err != nil {
				return nil, err
			}
			u.Time = date
			file.URLs = append(file.URLs, u)
		case "email":
			var e models.Email
			if err := rebuildLeak(src, r.src, &e); err != nil {
				return nil, err
			}
			e.Time = date
			file.Emails = append(file.Emails, e)
		case "phone":
			var p models.Phone
			if err := rebuildLeak(src, r.src, &p); err != nil {
				return nil, err
			}
			p.Time = date
			file.Phones = append(file.Phones, p)
		case "document":
			var doc models.Document
			if err := rebuildLeak(src, r.src, &doc); err != nil {
				return nil, err
			}
			doc.Time = date
			file.Documents = append(file.Documents, doc)
		}
	}

	return file, nil
}

// elkRefPointerFields are the reference fields that address the leak instead of
// describing it. They are dropped before the reference is merged back, so they
// cannot collide with a model field.
var elkRefPointerFields = []string{"file_id", "leak_id", "type", "bucket", "indexed_at"}

// rebuildLeak merges the deduplicated leak value with the occurrence context of
// one reference and decodes the result into v. Both sides use the model's own
// json tags (LeakDoc and RefDoc are built from them), so the merged object
// unmarshals straight back into the model; fields neither side stores -- the
// database ids, for one -- are simply absent and stay zero.
func rebuildLeak(leak json.RawMessage, ref map[string]json.RawMessage, v interface{}) error {
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(leak, &m); err != nil {
		return err
	}
	for k, raw := range ref {
		if tools.SliceHasStr(elkRefPointerFields, k) {
			continue
		}
		m[k] = raw
	}

	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
