package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	elk "github.com/elastic/go-elasticsearch/v8"
	"github.com/helviojunior/intelparser/internal/ascii"
	"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/models"
	"github.com/helviojunior/intelparser/pkg/writers"
	"github.com/spf13/cobra"
)

var migrateElkFlags = struct {
	srcIndex    string
	elasticURI  string
	debug       bool
	limit       int
	batchFiles  int
	batchMB     int
	readWorkers int
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
names must differ, since the old and new leak indices share suffixes. Replay
order does not matter: the writer reconstructs inserted_at / last_reference_at
with a server-side min/max, so files can be migrated concurrently (ELK_WORKERS,
default 4).

The source is read the way elasticdump does it: a _count request first, so
progress is reported against a known total, then fixed-size pages of --limit
documents (default 500). Raise --limit to trade memory and heap pressure on the
source cluster for fewer round-trips.

Leaks are not fetched one file at a time: files are grouped into batches
(--batch-files, capped by --batch-mb of source content) and each batch pulls its
leaks with a single terms query per leak index, which is what keeps the
round-trip count proportional to the data rather than to the file count.

File content is the one thing still read per file, and it is by far the largest,
so --read-workers of them are read at a time. Watch the ELK metrics line to
balance it against ELK_WORKERS: a queue sitting at 0 means the reading side is
the limit, a full queue means the writing side (or the cluster) is.`)),
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
		if migrateElkFlags.batchFiles < 1 || migrateElkFlags.batchFiles > 10000 {
			return errors.New("--batch-files must be between 1 and 10000")
		}
		if migrateElkFlags.batchMB < 1 {
			return errors.New("--batch-mb must be at least 1")
		}
		if migrateElkFlags.readWorkers < 1 || migrateElkFlags.readWorkers > 64 {
			return errors.New("--read-workers must be between 1 and 64")
		}
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		// The leak dates are reconciled server-side (min inserted_at, max
		// last_reference_at), so the replay no longer has to be ordered and the
		// writer can use its normal worker pool. Only the queue is kept short:
		// every queued file holds its full content plus all of its leaks, so a
		// deep queue here is a memory problem, not a throughput win.
		if _, ok := os.LookupEnv("ELK_QUEUE_SIZE"); !ok {
			os.Setenv("ELK_QUEUE_SIZE", "16")
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

		if err := migrateElastic(writer, migrateElkFlags.srcIndex, migrateElkFlags.limit,
			migrateElkFlags.batchFiles, migrateElkFlags.batchMB*1024*1024,
			migrateElkFlags.readWorkers); err != nil {
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
	migrateElkCmd.Flags().IntVar(&migrateElkFlags.batchFiles, "batch-files", 500, "How many files to resolve leaks for per terms query (1-10000)")
	migrateElkCmd.Flags().IntVar(&migrateElkFlags.batchMB, "batch-mb", 64, "Cap a batch once the source files in it add up to this many MB, so one huge file does not blow up memory (two batches are held at a time)")
	migrateElkCmd.Flags().IntVar(&migrateElkFlags.readWorkers, "read-workers", 8, "How many file contents to read from the source concurrently (1-64)")
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

// leakSuffixes are the five old-model leak indices hanging off the source index
// base, in the order they are reported.
var leakSuffixes = []string{"_creds", "_urls", "_emails", "_phone", "_document"}

// activeLeakSuffixes is leakSuffixes minus the indices --disable-url and
// --disable-email turn off, so a run that suppresses them neither counts nor
// replays those leaks.
func activeLeakSuffixes() []string {
	out := make([]string, 0, len(leakSuffixes))
	for _, suffix := range leakSuffixes {
		if suffix == "_urls" && rptDisableUrl {
			continue
		}
		if suffix == "_emails" && rptDisableEmail {
			continue
		}
		out = append(out, suffix)
	}
	return out
}

// migrateElastic reads the old-model dataset under srcIndex and replays every
// file (with its leaks) through the new writer.
//
// The file listing follows the elasticdump shape: a _count request first, so
// progress can be reported against a known total from the very first page
// instead of only after the last one, then fixed-size pages of `limit`
// documents.
//
// Leaks are then resolved in batches rather than per file. Resolving them one
// file at a time cost a search (plus the two round-trips a scroll adds on top)
// against each of the five leak indices for every single file, which for a
// dataset of tens of thousands of mostly-small files is hundreds of thousands
// of requests spent on lookups that mostly return nothing.
func migrateElastic(writer *writers.ElasticWriter, srcIndex string, limit, batchFiles, batchBytes, readWorkers int) error {
	client := writer.Client

	// --disable-url / --disable-email drop the matching source index from the
	// whole replay: it is not counted, not queried per batch, and its leaks
	// never reach the writer.
	suffixes := activeLeakSuffixes()
	if len(suffixes) != len(leakSuffixes) {
		skipped := make([]string, 0, 2)
		if rptDisableUrl {
			skipped = append(skipped, "urls")
		}
		if rptDisableEmail {
			skipped = append(skipped, "emails")
		}
		log.Infof("Skipping the %s leak indices of %q", strings.Join(skipped, " and "), srcIndex)
	}

	total, err := countDocs(client, srcIndex)
	if err != nil {
		return fmt.Errorf("counting source file index %q: %w", srcIndex, err)
	}
	if total == 0 {
		return fmt.Errorf("no documents found in source file index %q", srcIndex)
	}
	log.Infof("Source file index %q holds %s documents; reading %s per request",
		srcIndex, tools.FormatInt64Comma(total), tools.FormatIntComma(limit))

	// Five _count requests, once, buy the only progress unit that means
	// anything here. Files differ by three orders of magnitude in how many
	// leaks they carry, so "600/33.981 files" says almost nothing about how far
	// along a run is; the leak count is the work itself. Not fatal if it fails
	// -- the replay just falls back to counting files.
	leakCounts, totalLeaks, err := countSourceLeaks(client, srcIndex, suffixes)
	if err != nil {
		log.Warnf("Could not count the source leak indices, progress will be reported in files only: %s", err)
	} else {
		parts := make([]string, 0, len(suffixes))
		for _, suffix := range suffixes {
			parts = append(parts, fmt.Sprintf("%s %s",
				strings.TrimPrefix(suffix, "_"), tools.FormatInt64Comma(leakCounts[suffix])))
		}
		log.Infof("Source leak indices hold %s documents (%s)",
			tools.FormatInt64Comma(totalLeaks), strings.Join(parts, ", "))
		// Every leak occurrence becomes two destination documents: an upsert
		// into the deduplicated leak index and a file<->leak reference.
		log.Infof("Migrating them writes about %s documents: %s leak upserts, %s references, %s file documents",
			tools.FormatInt64Comma(2*totalLeaks+total), tools.FormatInt64Comma(totalLeaks),
			tools.FormatInt64Comma(totalLeaks), tools.FormatInt64Comma(total))
	}

	files := make([]fileMeta, 0, total)
	readProgress := newProgressTicker(limit * 10)
	// Same request body as elasticdump's scan — match_all sorted by _doc, which
	// is the cheapest possible order because it follows Lucene's own document
	// order and skips scoring entirely. content is the one field left out: the
	// listing is held in memory to be batched, and file bodies would not fit;
	// it is fetched per file in buildFile instead.
	listBody := `{"query":{"match_all":{}},"stored_fields":[],"_source":{"excludes":["content"]},"sort":["_doc"],"track_total_hits":true}`
	err = searchAll(client, srcIndex, listBody, limit, func(id string, src json.RawMessage) error {
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

	log.Infof("Migrating %s files from %q to %q (batches of up to %s files / %s, %d readers)",
		tools.FormatIntComma(len(files)), srcIndex, writer.Index,
		tools.FormatIntComma(batchFiles), tools.Bytes(uint64(batchBytes)), readWorkers)

	queueProgress := newProgressTicker(25)
	skipped := 0
	processed := 0
	queuedLeaks := int64(0)
	replayStart := time.Now()

	// Resolve each batch's leaks one batch ahead of the one being replayed.
	// The terms queries are a barrier -- nothing is written while they run --
	// and at a few hundred files per batch that barrier comes round often
	// enough to matter. The channel holds a single batch, so at most two are
	// ever in memory: the one being replayed and the one already fetched.
	jobs := make(chan *batchJob, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(jobs)
		for start := 0; start < len(files); {
			// A batch is capped both by file count and by how much source
			// content it represents: the leaks of the whole batch are held in
			// memory at once, and a single file can hold millions of them, so
			// one very large file ends up in a batch of its own.
			end := start + 1
			acc := int(files[start].doc.Size)
			for end < len(files) && end-start < batchFiles && acc < batchBytes {
				acc += int(files[end].doc.Size)
				end++
			}
			batch := files[start:end]
			start = end

			leaks, err := fetchBatchLeaks(client, srcIndex, batch, limit)
			select {
			case jobs <- &batchJob{batch: batch, leaks: leaks, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for job := range jobs {
		if job.err != nil {
			return job.err
		}
		batch, leaks := job.batch, job.leaks

		// Read the batch's file contents concurrently. This is the only part
		// of the replay left that runs per file, and content is the biggest
		// thing the migration reads, so doing it one at a time left the writer
		// pool idle -- an empty queue with sixteen workers waiting on it. Order
		// stopped mattering when the leak dates moved server-side, so the only
		// bound left is memory: at most readWorkers contents in flight, plus
		// whatever the writer queue holds.
		work := make(chan fileMeta)
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			writeErr error
		)
		for w := 0; w < readWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for fm := range work {
					file, err := buildFile(client, srcIndex, fm, leaks[fm.id])

					n := int64(0)
					if err == nil {
						n = int64(len(file.Credentials) + len(file.URLs) +
							len(file.Emails) + len(file.Phones) + len(file.Documents))
					}

					mu.Lock()
					processed++
					at := processed
					queuedLeaks += n
					leaksAt := queuedLeaks
					if err != nil {
						skipped++
					}
					due := queueProgress.due(at, len(files))
					mu.Unlock()

					if err != nil {
						// One unreadable file should not throw away the hours
						// already spent on the rest of the dataset: every id is
						// deterministic, so a later re-run rewrites exactly the
						// same documents and can pick these up.
						log.Errorf("Skipping file %s (%s): %s", fm.id, fm.doc.FileName, err)
					} else if err := writer.Write(file); err != nil {
						mu.Lock()
						if writeErr == nil {
							writeErr = err
						}
						mu.Unlock()
					}

					if due {
						log.Info(progressLine(at, len(files), leaksAt, totalLeaks, time.Since(replayStart)))
					}
				}
			}()
		}
		for _, fm := range batch {
			work <- fm
		}
		close(work)
		wg.Wait()
		if writeErr != nil {
			return writeErr
		}
	}

	if skipped > 0 {
		log.Warnf("%d file(s) were skipped after read errors; re-run the migration to pick them up", skipped)
	}
	return nil
}

// fileMeta is one entry of the in-memory file listing: the old document _id
// (the file fingerprint) plus its metadata.
type fileMeta struct {
	id  string
	doc *oldFileDoc
}

// countSourceLeaks asks each source leak index how many documents it holds. The
// five counts run concurrently; a missing index counts as zero (countDocs
// treats a 404 as an empty index), so a dataset without, say, a _phone index is
// not an error.
func countSourceLeaks(client *elk.Client, srcIndex string, suffixes []string) (map[string]int64, int64, error) {
	counts := make([]int64, len(suffixes))
	errs := make([]error, len(suffixes))
	var wg sync.WaitGroup
	for i, suffix := range suffixes {
		wg.Add(1)
		go func(i int, suffix string) {
			defer wg.Done()
			counts[i], errs[i] = countDocs(client, srcIndex+suffix)
		}(i, suffix)
	}
	wg.Wait()

	out := make(map[string]int64, len(suffixes))
	var total int64
	for i, suffix := range suffixes {
		if errs[i] != nil {
			return nil, 0, fmt.Errorf("counting %s: %w", srcIndex+suffix, errs[i])
		}
		out[suffix] = counts[i]
		total += counts[i]
	}
	return out, total, nil
}

// progressLine renders the replay progress. It leads with the file count for
// continuity, but the number that carries information is the leak fraction --
// and the projection is built from it, not from the files. The rate is the rate
// leaks are being handed to the writer; reader and writer track each other
// closely enough for that to be the run's rate.
func progressLine(files, totalFiles int, leaks, totalLeaks int64, elapsed time.Duration) string {
	line := fmt.Sprintf("Queued %s/%s files",
		tools.FormatIntComma(files), tools.FormatIntComma(totalFiles))
	if totalLeaks <= 0 {
		return line + " for migration"
	}

	line += fmt.Sprintf(", %s/%s leaks (%.1f%%)",
		tools.FormatInt64Comma(leaks), tools.FormatInt64Comma(totalLeaks),
		float64(leaks)/float64(totalLeaks)*100)

	rate := float64(leaks) / elapsed.Seconds()
	if leaks <= 0 || elapsed <= 0 || rate <= 0 {
		return line
	}
	line += fmt.Sprintf(" at %s leaks/s", tools.FormatInt64Comma(int64(rate)))
	if remaining := totalLeaks - leaks; remaining > 0 {
		line += fmt.Sprintf(", ETA %s", humanDuration(time.Duration(float64(remaining)/rate*float64(time.Second))))
	}
	return line
}

// humanDuration renders a duration the coarse way a progress line wants it.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	d = d.Round(time.Minute)
	if h := int(d / time.Hour); h > 0 {
		return fmt.Sprintf("%dh%02dm", h, int((d%time.Hour)/time.Minute))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

// batchJob is one batch of files with its leaks already resolved, handed from
// the prefetching goroutine to the replay loop. err carries a failed fetch
// across so the loop can stop on it.
type batchJob struct {
	batch []fileMeta
	leaks map[string]*batchLeaks
	err   error
}

// batchLeaks holds every leak belonging to one file, bucketed by type.
type batchLeaks struct {
	creds  []models.Credential
	urls   []models.URL
	emails []models.Email
	phones []models.Phone
	docs   []models.Document
}

// fetchBatchLeaks resolves the leaks of a whole batch of files with one terms
// query per leak index, and returns them bucketed by file id. The five indices
// are queried concurrently: they are independent, and the source cluster has no
// trouble serving five scans at once.
func fetchBatchLeaks(client *elk.Client, srcIndex string, batch []fileMeta, limit int) (map[string]*batchLeaks, error) {
	ids := make([]string, len(batch))
	for i, fm := range batch {
		ids[i] = fm.id
	}
	body := termsBody(ids)

	var (
		creds  map[string][]models.Credential
		urls   map[string][]models.URL
		emails map[string][]models.Email
		phones map[string][]models.Phone
		docs   map[string][]models.Document
	)
	errs := make([]error, 5)
	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		creds, errs[0] = fetchLeaks[models.Credential](client, srcIndex+"_creds", body, limit)
	}()
	go func() {
		defer wg.Done()
		if rptDisableUrl {
			return
		}
		urls, errs[1] = fetchLeaks[models.URL](client, srcIndex+"_urls", body, limit)
	}()
	go func() {
		defer wg.Done()
		if rptDisableEmail {
			return
		}
		emails, errs[2] = fetchLeaks[models.Email](client, srcIndex+"_emails", body, limit)
	}()
	go func() {
		defer wg.Done()
		phones, errs[3] = fetchLeaks[models.Phone](client, srcIndex+"_phone", body, limit)
	}()
	go func() {
		defer wg.Done()
		docs, errs[4] = fetchLeaks[models.Document](client, srcIndex+"_document", body, limit)
	}()
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, fmt.Errorf("reading leaks for a batch of %d files from %q: %w", len(batch), srcIndex, e)
		}
	}

	out := make(map[string]*batchLeaks, len(batch))
	bucket := func(id string) *batchLeaks {
		b := out[id]
		if b == nil {
			b = &batchLeaks{}
			out[id] = b
		}
		return b
	}
	for id, v := range creds {
		bucket(id).creds = v
	}
	for id, v := range urls {
		bucket(id).urls = v
	}
	for id, v := range emails {
		bucket(id).emails = v
	}
	for id, v := range phones {
		bucket(id).phones = v
	}
	for id, v := range docs {
		bucket(id).docs = v
	}
	return out, nil
}

// fetchLeaks runs one leak query over index and groups the decoded leaks by the
// file they belong to.
func fetchLeaks[T any](client *elk.Client, index, body string, limit int) (map[string][]T, error) {
	out := map[string][]T{}
	err := searchAll(client, index, body, limit, func(_ string, src json.RawMessage) error {
		var v T
		fileID, err := decodeLeak(src, &v)
		if err != nil {
			return err
		}
		out[fileID] = append(out[fileID], v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// termsBody is the batch leak query: every leak whose file_id is one of ids,
// in _doc order (no scoring). track_total_hits lets searchAll tell a complete
// single-page answer from a truncated one.
func termsBody(ids []string) string {
	b, _ := json.Marshal(ids)
	return fmt.Sprintf(`{"query":{"terms":{"file_id":%s}},"stored_fields":[],"_source":true,"sort":["_doc"],"track_total_hits":true}`, b)
}

// buildFile reconstructs a models.File from its old metadata document, the
// leaks already resolved for it by fetchBatchLeaks, and its content — the one
// thing still fetched per file, since holding the bodies of a whole batch in
// memory is exactly what the batching avoids.
func buildFile(client *elk.Client, srcIndex string, fm fileMeta, leaks *batchLeaks) (*models.File, error) {
	content, err := getFileContent(client, srcIndex, fm.id)
	if err != nil {
		return nil, err
	}

	o := fm.doc
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
		Fingerprint: fm.id,
		Content:     content,
	}
	if leaks != nil {
		file.Credentials = leaks.creds
		file.URLs = leaks.urls
		file.Emails = leaks.emails
		file.Phones = leaks.phones
		file.Documents = leaks.docs
	}
	return file, nil
}

// decodeLeak unmarshals a stored old-model leak document into a models leak
// type and returns the file id it was attached to. The intrinsic value fields
// (json tags) map straight through; the envelope fields the old writer appended
// are not fields of the model, so encoding/json skips them on its own.
//
// The one exception is file_id, which the old model stored as the file
// fingerprint -- a string, where the model's FieldID is a uint. That single
// clash is expected on every document and is tolerated: encoding/json records
// an UnmarshalTypeError for the offending field and carries on decoding the
// rest, so the leak still arrives complete.
//
// This runs once per source leak document -- tens of millions of times in a
// real migration -- so it decodes straight into the target instead of routing
// through a map[string]json.RawMessage and re-marshalling it. That round-trip
// allocated the whole document three extra times per leak, and on a dataset
// this size the garbage it produced was a substantial part of what the
// collector had to chase.
func decodeLeak(src json.RawMessage, v interface{}) (string, error) {
	var envelope struct {
		FileID string `json:"file_id"`
	}
	if err := json.Unmarshal(src, &envelope); err != nil {
		return "", err
	}

	if err := json.Unmarshal(src, v); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) || typeErr.Field != "file_id" {
			return "", err
		}
	}
	return envelope.FileID, nil
}

// getFileContent fetches only the content field of a single old file document.
// A GET by _id routes straight to the shard that owns the document; the search
// this used to run was broadcast to every shard and, wrapped in a scroll, cost
// three round-trips to return one field of one document.
func getFileContent(client *elk.Client, index, id string) (string, error) {
	res, err := client.Get(index, id,
		client.Get.WithContext(context.Background()),
		client.Get.WithSourceIncludes("content"))
	if err != nil {
		return "", err
	}
	status := res.StatusCode
	var out struct {
		Source struct {
			Content string `json:"content"`
		} `json:"_source"`
	}
	decErr := json.NewDecoder(res.Body).Decode(&out)
	drainClose(res.Body)

	if status == 404 {
		return "", nil
	}
	if status != 200 {
		return "", fmt.Errorf("get %s/%s failed with status %d", index, id, status)
	}
	if decErr != nil {
		return "", decErr
	}
	return out.Source.Content, nil
}

type esScrollResp struct {
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
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

	status := res.StatusCode
	isErr := res.IsError()
	var out struct {
		Count int64 `json:"count"`
	}
	decErr := json.NewDecoder(res.Body).Decode(&out)
	drainClose(res.Body)

	if status == 404 {
		return 0, nil
	}
	if isErr {
		return 0, fmt.Errorf("count on %s failed with status %d", index, status)
	}
	if decErr != nil {
		return 0, decErr
	}
	return out.Count, nil
}

// elkMaxResultWindow is Elasticsearch's default index.max_result_window: the
// largest size a plain (non-scrolled, non-paged) search will accept. A cluster
// can lower it, so a search sized against it is always allowed to fail back to
// a scroll rather than being trusted blindly.
const elkMaxResultWindow = 10000

// searchAll runs body over index and invokes fn for every hit.
//
// It avoids scrolling wherever it can. body carries track_total_hits, so the
// first response says outright how many hits exist: if that one page already
// held them all the read is a single round-trip, and if it did not but they
// would still fit in one request, the query is simply re-issued sized to the
// total. A scroll is the last resort, for result sets past the result window.
// It costs three round-trips at minimum (the search, the trailing empty page
// that ends the loop, and the clear) plus a search context on the source
// cluster, which is a lot to pay for the lookups that dominate a migration --
// most of which return nothing at all.
//
// The fallbacks re-run the query from the start, so fn is only ever called once
// the response it is being fed is known to be complete. A missing index (404)
// is an empty result set, not an error.
func searchAll(client *elk.Client, index, body string, size int, fn func(id string, src json.RawMessage) error) error {
	emit := func(page *esScrollResp) error {
		for _, h := range page.Hits.Hits {
			if err := fn(h.ID, h.Source); err != nil {
				return err
			}
		}
		return nil
	}

	page, missing, err := searchPage(client, index, body, size)
	if err != nil {
		return err
	}
	if missing {
		return nil
	}
	if int64(len(page.Hits.Hits)) >= page.Hits.Total.Value {
		return emit(page)
	}

	if total := page.Hits.Total.Value; total <= elkMaxResultWindow {
		full, missing, err := searchPage(client, index, body, int(total))
		// An error here means the cluster would not serve a page that size
		// (a lowered index.max_result_window, say) — the scroll below still
		// will, so it is not worth failing the migration over.
		if err == nil && !missing && int64(len(full.Hits.Hits)) >= full.Hits.Total.Value {
			return emit(full)
		}
	}

	return scrollAll(client, index, body, size, fn)
}

// searchPage runs one plain search and returns the decoded response. missing is
// true when the index does not exist.
func searchPage(client *elk.Client, index, body string, size int) (*esScrollResp, bool, error) {
	res, err := client.Search(
		client.Search.WithContext(context.Background()),
		client.Search.WithIndex(index),
		client.Search.WithBody(strings.NewReader(body)),
		client.Search.WithSize(size),
	)
	if err != nil {
		return nil, false, err
	}

	var page esScrollResp
	status := res.StatusCode
	isErr := res.IsError()
	decErr := json.NewDecoder(res.Body).Decode(&page)
	drainClose(res.Body)

	if status == 404 {
		return nil, true, nil
	}
	if isErr {
		return nil, false, fmt.Errorf("search %s failed with status %d", index, status)
	}
	if decErr != nil {
		return nil, false, decErr
	}
	return &page, false, nil
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
	drainClose(res.Body)
	if status == 404 {
		return nil
	}
	if isErr {
		return fmt.Errorf("search %s failed with status %d", index, status)
	}
	if decErr != nil {
		return decErr
	}

	// Reported by the first response only (track_total_hits); used to stop as
	// soon as the last hit has been handed over, instead of paying one more
	// scroll request just to be told the result set is exhausted.
	total := page.Hits.Total.Value
	seen := int64(0)

	scrollID := page.ScrollID
	defer func() {
		if scrollID != "" {
			if cr, e := client.ClearScroll(client.ClearScroll.WithScrollID(scrollID)); e == nil {
				drainClose(cr.Body)
			}
		}
	}()

	for len(page.Hits.Hits) > 0 {
		for _, h := range page.Hits.Hits {
			if err := fn(h.ID, h.Source); err != nil {
				return err
			}
		}
		seen += int64(len(page.Hits.Hits))
		if total > 0 && seen >= total {
			return nil
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
		drainClose(sr.Body)
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

// drainClose reads a response body to EOF before closing it. Go only hands a
// keep-alive connection back to the pool once its body has been fully consumed;
// closing early forces the next request to redo the TCP (and, over HTTPS, TLS)
// handshake.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// jsonString returns s as a JSON string literal (quoted, escaped).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
