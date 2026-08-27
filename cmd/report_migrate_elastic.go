package cmd

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "sort"
    "strings"
    "time"

    "golang.org/x/term"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/tools"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/models"
    "github.com/helviojunior/intelparser/pkg/writers"
    elk "github.com/elastic/go-elasticsearch/v8"
    "github.com/spf13/cobra"
)

var migrateElkFlags = struct {
    srcIndex   string
    elasticURI string
    debug      bool
    limit      int
}{}

var migrateElkCmd = &cobra.Command{
    Use:   "migrate-elastic",
    Short: "Migrate an old-model Elasticsearch dataset to the new file/leak/reference structure",
    Long: ascii.LogoHelp(ascii.Markdown(`
# report migrate-elastic

Migrate an existing (old-model) Elasticsearch dataset to the restructured
model in place on the same cluster:

  - a single global file index (fingerprint dropped as a field);
  - global, deduplicated leak indices (_creds/_urls/_emails/_phone/_document)
    keyed by the content hash, each carrying inserted_at + last_reference_at;
  - monthly file<->leak reference indices (<dst>_ref_YYYY-MM).

The source (--source-index) and destination (--elasticsearch-uri index) base
names must differ, since the old and new leak indices share suffixes. Files are
replayed in chronological order (by indexed_at) through the normal writer, so
inserted_at / last_reference_at reconstruct correctly.

The source is read the way elasticdump does it: a _count request first, so
progress is reported against a known total, then fixed-size pages of --limit
documents (default 500). Raise --limit to trade memory and heap pressure on the
source cluster for fewer round-trips.`)),
    Example: ascii.Markdown(`
   - intelparser report migrate-elastic --source-index intelparser --elasticsearch-uri http://localhost:9200/intelparser_v2
   - intelparser report migrate-elastic --source-index testes --elasticsearch-uri http://user:pass@host:9200/testes_v2
   - intelparser report migrate-elastic --source-index intelparser --elasticsearch-uri http://localhost:9200/intelparser_v2 --limit 2000`),
    PreRunE: func(cmd *cobra.Command, args []string) error {
        if migrateElkFlags.srcIndex == "" {
            return errors.New("--source-index is required")
        }
        if migrateElkFlags.limit < 1 || migrateElkFlags.limit > 10000 {
            return errors.New("--limit must be between 1 and 10000")
        }
        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {
        // Force an ordered, memory-bounded replay: a single writer worker means
        // the queue is drained FIFO, and feeding files in chronological order
        // then guarantees inserted_at = earliest reference and
        // last_reference_at = latest reference for every deduplicated leak.
        os.Setenv("ELK_WORKERS", "1")
        if _, ok := os.LookupEnv("ELK_QUEUE_SIZE"); !ok {
            os.Setenv("ELK_QUEUE_SIZE", "8")
        }

        log.Info("Connecting to Elasticsearch and preparing destination indices...")
        writer, err := writers.NewElasticWriter(migrateElkFlags.elasticURI, migrateElkFlags.debug)
        if err != nil {
            log.Error("could not get an elastic writer up", "err", err)
            return
        }

        if migrateElkFlags.srcIndex == writer.Index {
            log.Error("source and destination index base names must differ",
                "source", migrateElkFlags.srcIndex, "destination", writer.Index)
            return
        }

        if err := migrateElastic(writer, migrateElkFlags.srcIndex, migrateElkFlags.limit); err != nil {
            log.Error("migration failed", "err", err)
            // still flush what was written so far
        }

        if err := writer.Flush(); err != nil {
            log.Error("flush failed", "err", err)
        }
        _ = writer.Finalize()
    },
}

func init() {
    reportCmd.AddCommand(migrateElkCmd)

    migrateElkCmd.Flags().StringVar(&migrateElkFlags.srcIndex, "source-index", "", "The OLD-model index base name to read from (same cluster), e.g. 'intelparser'")
    migrateElkCmd.Flags().StringVar(&migrateElkFlags.elasticURI, "elasticsearch-uri", "http://localhost:9200/intelparser_v2", "Destination Elasticsearch URI (new-model index base). Must differ from --source-index.")
    migrateElkCmd.Flags().BoolVar(&migrateElkFlags.debug, "write-elasticsearch-enable-debug", false, "Enable ElasticSearch writer debug logging")
    migrateElkCmd.Flags().IntVar(&migrateElkFlags.limit, "limit", 500, "How many documents to pull per search request (1-10000)")
}

// progressTicker paces the migration's progress lines. On a terminal the run is
// being watched and the lines scroll past, so one every few items is useful.
// Redirected to a file or a CI log every line is kept forever, and a line per 25
// files out of 16k is noise — there the same progress is reported at most once
// per interval instead. Mirrors the cadence split the ConvStatus display uses.
type progressTicker struct {
    isTerminal bool
    every      int           // on a terminal: emit every N items
    interval   time.Duration // otherwise: at most one line per interval
    last       time.Time
}

func newProgressTicker(every int) *progressTicker {
    return &progressTicker{
        isTerminal: term.IsTerminal(int(os.Stdin.Fd())),
        every:      every,
        interval:   30 * time.Second,
    }
}

// due reports whether progress at item n of total should be logged now. The
// last item always reports, so a run never ends on a stale partial count.
func (p *progressTicker) due(n, total int) bool {
    if n >= total {
        return true
    }
    if p.isTerminal {
        return p.every > 0 && n%p.every == 0
    }
    if time.Since(p.last) < p.interval {
        return false
    }
    p.last = time.Now()
    return true
}

// oldFileDoc maps the fields stored by the old-model file index. The old
// document _id is the file fingerprint; the leak date was stored as leak_date
// (the File struct's own json tag is `date`, hence the explicit mapping here).
type oldFileDoc struct {
    Provider   string    `json:"provider"`
    FilePath   string    `json:"file_path"`
    FileName   string    `json:"file_name"`
    Name       string    `json:"name"`
    LeakDate   time.Time `json:"leak_date"`
    Bucket     string    `json:"bucket"`
    MediaType  string    `json:"media_type"`
    IndexedAt  time.Time `json:"indexed_at"`
    Size       uint      `json:"size"`
    ProviderId string    `json:"provider_id"`
    MIMEType   string    `json:"mime_type"`
}

// migrateElastic reads the old-model dataset under srcIndex and replays every
// file (with its leaks) through the new writer in chronological order.
//
// The scan follows the elasticdump shape: a _count request first, so progress
// can be reported against a known total from the very first page instead of
// only after the last one, then fixed-size pages of `limit` documents.
func migrateElastic(writer *writers.ElasticWriter, srcIndex string, limit int) error {
    client := writer.Client

    total, err := countDocs(client, srcIndex)
    if err != nil {
        return fmt.Errorf("counting source file index %q: %w", srcIndex, err)
    }
    if total == 0 {
        return fmt.Errorf("no documents found in source file index %q", srcIndex)
    }
    log.Infof("Source file index %q holds %s documents; reading %s per request",
        srcIndex, tools.FormatInt64Comma(total), tools.FormatIntComma(limit))

    type fileMeta struct {
        id  string
        doc *oldFileDoc
    }

    files := make([]fileMeta, 0, total)
    readProgress := newProgressTicker(limit * 10)
    // Same request body as elasticdump's scan — match_all sorted by _doc, which
    // is the cheapest possible order because it follows Lucene's own document
    // order and skips scoring entirely. content is the one field left out: the
    // listing is held in memory to be sorted chronologically, and file bodies
    // would not fit; it is fetched per file in buildFileWithLeaks instead.
    listBody := `{"query":{"match_all":{}},"stored_fields":[],"_source":{"excludes":["content"]},"sort":["_doc"]}`
    err = scrollAll(client, srcIndex, listBody, limit, func(id string, src json.RawMessage) error {
        var o oldFileDoc
        if err := json.Unmarshal(src, &o); err != nil {
            return err
        }
        files = append(files, fileMeta{id: id, doc: &o})
        if readProgress.due(len(files), int(total)) {
            log.Infof("Read %s/%s file documents", tools.FormatIntComma(len(files)), tools.FormatInt64Comma(total))
        }
        return nil
    })
    if err != nil {
        return fmt.Errorf("reading source file index %q: %w", srcIndex, err)
    }
    if len(files) == 0 {
        return fmt.Errorf("no documents found in source file index %q", srcIndex)
    }
    if int64(len(files)) != total {
        // Not fatal: the count is a point-in-time snapshot and the index may be
        // written to concurrently. Worth surfacing so a silent shortfall is not
        // mistaken for a complete migration.
        log.Warnf("Read %d file documents but _count reported %d; the source index may be changing during the migration",
            len(files), total)
    }

    // Chronological order so the FIFO single-worker replay yields correct
    // inserted_at / last_reference_at.
    sort.SliceStable(files, func(i, j int) bool {
        return files[i].doc.IndexedAt.Before(files[j].doc.IndexedAt)
    })

    log.Infof("Migrating %s files from %q to %q (chronological replay, single worker)",
        tools.FormatIntComma(len(files)), srcIndex, writer.Index)

    queueProgress := newProgressTicker(25)
    for i, fm := range files {
        file, err := buildFileWithLeaks(client, srcIndex, fm.id, fm.doc, limit)
        if err != nil {
            return fmt.Errorf("building file %s (%s): %w", fm.id, fm.doc.FileName, err)
        }
        if err := writer.Write(file); err != nil {
            return err
        }
        if queueProgress.due(i+1, len(files)) {
            log.Infof("Queued %s/%s files for migration",
                tools.FormatIntComma(i+1), tools.FormatIntComma(len(files)))
        }
    }
    return nil
}

// buildFileWithLeaks reconstructs a models.File (metadata + content) and pulls
// every leak that references it out of the old per-type leak indices.
func buildFileWithLeaks(client *elk.Client, srcIndex, fileID string, o *oldFileDoc, limit int) (*models.File, error) {
    content, err := getFileContent(client, srcIndex, fileID)
    if err != nil {
        return nil, err
    }

    file := &models.File{
        Provider:    o.Provider,
        FilePath:    o.FilePath,
        FileName:    o.FileName,
        Name:        o.Name,
        Date:        o.LeakDate,
        Bucket:      o.Bucket,
        MediaType:   o.MediaType,
        IndexedAt:   o.IndexedAt,
        Size:        o.Size,
        ProviderId:  o.ProviderId,
        MIMEType:    o.MIMEType,
        Fingerprint: fileID,
        Content:     content,
    }

    // Same shape as the file scan: no stored fields, full _source, _doc order.
    q := fmt.Sprintf(`{"query":{"term":{"file_id":%s}},"stored_fields":[],"_source":true,"sort":["_doc"]}`, jsonString(fileID))

    if err := scrollAll(client, srcIndex+"_creds", q, limit, func(_ string, src json.RawMessage) error {
        var c models.Credential
        if err := decodeLeak(src, &c); err != nil {
            return err
        }
        file.Credentials = append(file.Credentials, c)
        return nil
    }); err != nil {
        return nil, err
    }

    if err := scrollAll(client, srcIndex+"_urls", q, limit, func(_ string, src json.RawMessage) error {
        var u models.URL
        if err := decodeLeak(src, &u); err != nil {
            return err
        }
        file.URLs = append(file.URLs, u)
        return nil
    }); err != nil {
        return nil, err
    }

    if err := scrollAll(client, srcIndex+"_emails", q, limit, func(_ string, src json.RawMessage) error {
        var e models.Email
        if err := decodeLeak(src, &e); err != nil {
            return err
        }
        file.Emails = append(file.Emails, e)
        return nil
    }); err != nil {
        return nil, err
    }

    if err := scrollAll(client, srcIndex+"_phone", q, limit, func(_ string, src json.RawMessage) error {
        var p models.Phone
        if err := decodeLeak(src, &p); err != nil {
            return err
        }
        file.Phones = append(file.Phones, p)
        return nil
    }); err != nil {
        return nil, err
    }

    if err := scrollAll(client, srcIndex+"_document", q, limit, func(_ string, src json.RawMessage) error {
        var d models.Document
        if err := decodeLeak(src, &d); err != nil {
            return err
        }
        file.Documents = append(file.Documents, d)
        return nil
    }); err != nil {
        return nil, err
    }

    return file, nil
}

// decodeLeak unmarshals a stored old-model leak document into a models leak
// type. The old envelope fields (file_id/bucket/fingerprint) that the old
// writer appended are dropped first: in particular file_id was stored as a
// string (the file fingerprint) and would fail to unmarshal into the model's
// uint FileID field. The intrinsic value fields (json tags) map straight
// through.
func decodeLeak(src json.RawMessage, v interface{}) error {
    var m map[string]json.RawMessage
    if err := json.Unmarshal(src, &m); err != nil {
        return err
    }
    delete(m, "file_id")
    delete(m, "fingerprint")
    delete(m, "bucket")
    b, err := json.Marshal(m)
    if err != nil {
        return err
    }
    return json.Unmarshal(b, v)
}

// getFileContent fetches only the content field of a single old file document.
func getFileContent(client *elk.Client, index, id string) (string, error) {
    body := fmt.Sprintf(`{"_source":{"includes":["content"]},"query":{"ids":{"values":[%s]}}}`, jsonString(id))
    var content string
    err := scrollAll(client, index, body, 1, func(_ string, src json.RawMessage) error {
        var o struct {
            Content string `json:"content"`
        }
        if err := json.Unmarshal(src, &o); err != nil {
            return err
        }
        content = o.Content
        return nil
    })
    return content, err
}

type esScrollResp struct {
    ScrollID string `json:"_scroll_id"`
    Hits     struct {
        Hits []struct {
            ID     string          `json:"_id"`
            Source json.RawMessage `json:"_source"`
        } `json:"hits"`
    } `json:"hits"`
}

// countDocs returns how many documents index holds, mirroring the _count call
// elasticdump issues before it starts dumping. A missing index (404) counts as
// zero rather than an error, matching scrollAll.
func countDocs(client *elk.Client, index string) (int64, error) {
    res, err := client.Count(
        client.Count.WithContext(context.Background()),
        client.Count.WithIndex(index),
        client.Count.WithBody(strings.NewReader(`{"query":{"match_all":{}}}`)),
    )
    if err != nil {
        return 0, err
    }
    defer res.Body.Close()

    if res.StatusCode == 404 {
        return 0, nil
    }
    if res.IsError() {
        return 0, fmt.Errorf("count on %s failed with status %d", index, res.StatusCode)
    }

    var out struct {
        Count int64 `json:"count"`
    }
    if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
        return 0, err
    }
    return out.Count, nil
}

// scrollAll runs a scrolled search over index and invokes fn for every hit,
// pulling size documents per request. A missing index (404) is treated as an
// empty result set (nothing to migrate for that leak type), not an error.
func scrollAll(client *elk.Client, index, body string, size int, fn func(id string, src json.RawMessage) error) error {
    res, err := client.Search(
        client.Search.WithContext(context.Background()),
        client.Search.WithIndex(index),
        client.Search.WithBody(strings.NewReader(body)),
        client.Search.WithScroll(2*time.Minute),
        client.Search.WithSize(size),
    )
    if err != nil {
        return err
    }

    var page esScrollResp
    status := res.StatusCode
    isErr := res.IsError()
    decErr := json.NewDecoder(res.Body).Decode(&page)
    res.Body.Close()
    if status == 404 {
        return nil
    }
    if isErr {
        return fmt.Errorf("search %s failed with status %d", index, status)
    }
    if decErr != nil {
        return decErr
    }

    scrollID := page.ScrollID
    defer func() {
        if scrollID != "" {
            if cr, e := client.ClearScroll(client.ClearScroll.WithScrollID(scrollID)); e == nil {
                cr.Body.Close()
            }
        }
    }()

    for len(page.Hits.Hits) > 0 {
        for _, h := range page.Hits.Hits {
            if err := fn(h.ID, h.Source); err != nil {
                return err
            }
        }

        sr, err := client.Scroll(
            client.Scroll.WithContext(context.Background()),
            client.Scroll.WithScrollID(scrollID),
            client.Scroll.WithScroll(2*time.Minute),
        )
        if err != nil {
            return err
        }
        srErr := sr.IsError()
        page = esScrollResp{}
        decErr := json.NewDecoder(sr.Body).Decode(&page)
        sr.Body.Close()
        if srErr {
            return fmt.Errorf("scroll on %s failed", index)
        }
        if decErr != nil {
            return decErr
        }
        scrollID = page.ScrollID
    }
    return nil
}

// jsonString returns s as a JSON string literal (quoted, escaped).
func jsonString(s string) string {
    b, _ := json.Marshal(s)
    return string(b)
}
