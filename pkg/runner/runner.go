package runner

import (
	"context"
	"errors"
	"log/slog"
	//"net/url"
	"os"
	"fmt"
	"sync"
	"time"
	"strings"
	"math"
	//"math/rand/v2"
    "path/filepath"
	"regexp"
	"bufio"
	"bytes"
	"io"
    //"os/signal"
    //"syscall"

    "golang.org/x/term"

	"github.com/h2non/filetype"
	"github.com/helviojunior/intelparser/internal/ascii"
	"github.com/helviojunior/intelparser/pkg/models"
	"github.com/helviojunior/intelparser/pkg/writers"
	ahocorasick "github.com/BobuSumisu/aho-corasick"
	"github.com/helviojunior/intelparser/pkg/runner/rules"
	"golang.org/x/exp/maps"
)

const (
	gitleaksAllowSignature = "gitleaks:allow"
	chunkSize              = 100 * 1_000 // 100kb
	maxPeekSize 		   = 25 * 1_000 // 10kb
)

var newLineRegexp = regexp.MustCompile("\n")

type Credential struct {
	Username string
	Password string
}

type Identifiers struct {
	Rules       []*rules.Rule
	Keywords    map[string]struct{}
}

// Runner is a runner that probes web targets using a driver
type Runner struct {
	Parser     ParserDriver

	// options for the Runner to consider
	options Options
	// writers are the result writers to use
	writers []writers.Writer
	// log handler
	log *slog.Logger

	// Files to scan.
	Files chan string

	// in case we need to bail
	ctx    context.Context
	cancel context.CancelFunc

	status *Status

	//Test id
	uid string

	Identifiers       Identifiers

	// prefilter is a ahocorasick struct used for doing efficient string
	// matching given a set of words (keywords from the rules in the config)
	prefilter ahocorasick.Trie

	// MaxDecodeDepths limits how many recursive decoding passes are allowed
	MaxDecodeDepth int

	// files larger than this will be skipped
	MaxTargetMegaBytes int
}

type Status struct {
	Parsed int
	Error int
    Url int
    Email int
    Credential int
	Skipped int
	Spin string
	Running bool
    IsTerminal bool
    log *slog.Logger
}

func (st *Status) Print() { 
    if st.IsTerminal {
    	st.Spin = ascii.GetNextSpinner(st.Spin)

    	fmt.Fprintf(os.Stderr, 
            "%s\n %s read: %d, failed: %d, ignored: %d               \n %s cred: %d, url: %d, email: %d\r\033[A\033[A", 
        	"                                                                        ",
        	ascii.ColoredSpin(st.Spin), 
            st.Parsed, 
            st.Error, 
            st.Skipped, 
            strings.Repeat(" ", 4 - len(st.Spin)),
            st.Credential, 
            st.Url, 
            st.Email)
    	
    }else{
        st.log.Info("STATUS", 
            "read", st.Parsed, "failed", st.Error, "ignored", st.Skipped, 
            "creds", st.Credential, "url", st.Url, "email", st.Email)
    }
} 

func (st *Status) AddResult(result *models.File) { 
    st.Parsed += 1
	if result.Failed {
		st.Error += 1
		return
	}
} 

// New gets a new Runner ready for probing.
// It's up to the caller to call Close() on the runner
func NewRunner(logger *slog.Logger, parser ParserDriver, opts Options, writers []writers.Writer) (*Runner, error) {

	ctx, cancel := context.WithCancel(context.Background())
	id := Identifiers{
		Rules: []*rules.Rule{},
		Keywords: make(map[string]struct{}),
	}
	id.LoadRules()

	return &Runner{
		Parser:     parser,
		options:    opts,
		writers:    writers,
		Files:      make(chan string),
		log:        logger,
		ctx:        ctx,
		cancel:     cancel,
		uid: 		fmt.Sprintf("%d", time.Now().UnixMilli()),
		Identifiers: id,
		prefilter:   *ahocorasick.NewTrieBuilder().AddStrings(maps.Keys(id.Keywords)).Build(),
		MaxDecodeDepth: 3,
		MaxTargetMegaBytes: 200,
		status:     &Status{
			Parsed: 0,
			Error: 0,
			Skipped: 0,
			Spin: "",
			Running: true,
            IsTerminal: term.IsTerminal(int(os.Stdin.Fd())),
            log: logger,
		},
	}, nil
}

func (id *Identifiers) LoadRules() error {
	id.Rules = []*rules.Rule{
		rules.Url(),
		rules.Email(),
        rules.Leak1(),
        rules.Leak2(),
        rules.Leak3(),
	}

	uniqueKeywords := make(map[string]struct{})
	for _, r := range id.Rules {
		for _, keyword := range r.Keywords {
			k := strings.ToLower(keyword)
			if _, ok := uniqueKeywords[k]; ok {
				continue
			}
			uniqueKeywords[k] = struct{}{}
		}
	}
	id.Keywords = uniqueKeywords

	return nil
}

// runWriters takes a result and passes it to writers
func (run *Runner) runWriters(result *models.File) error {
	for _, writer := range run.writers {
		if err := writer.Write(result); err != nil {
			return err
		}
	}

	return nil
}

func (run *Runner) AddSkipped() {
	run.status.Skipped += 1
	run.status.Parsed += 1
}

func (run *Runner) ParsePositionalFile(file_path string) error {
	_, err := run.Parser.ParseFile(run, file_path)
	return err
}

// Run executes the runner, processing targets as they arrive
// in the Targets channel
func (run *Runner) Run() Status {
    defer run.Close()

    ascii.HideCursor()
	wg := sync.WaitGroup{}
	swg := sync.WaitGroup{}

    /*
    c := make(chan os.Signal)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-c
        fmt.Fprintf(os.Stderr, "\n%s\n", 
            "                                                                                ",
        )
        run.log.Warn("interrupted, shutting down...                            \n")

        run.status.Running = false
    }()
    */

	if !run.options.Logging.Silence {
		swg.Add(1)
		go func() {
	        defer swg.Done()
			for run.status.Running {
				select {
					case <-run.ctx.Done():
						return
					default:
			        	run.status.Print()
			        	if run.status.IsTerminal {
                            time.Sleep(time.Duration(time.Second / 4))
                        }else{
                            time.Sleep(time.Duration(time.Second * 30))
                        }
			    }
	        }
	    }()
	}

	// will spawn Parser.Theads number of "workers" as goroutines
	for w := 0; w < run.options.Parser.Threads; w++ {
		wg.Add(1)

		// start a worker
		go func() {
			defer wg.Done()
			for run.status.Running {
				select {
				case <-run.ctx.Done():
					return
				case file_path, ok := <-run.Files:
					if !ok || !run.status.Running {
						return
					}
                    file_name := filepath.Base(file_path)
					logger := run.log.With("file", file_name)
					
                    logger.Debug("Indexing")

					file, err := run.Parser.ParseFile(run, file_path)
					if err != nil {
						file.Failed = true
						file.FailedReason = err.Error()
						logger.Error("failed to parse file", "err", err)
						run.status.AddResult(file)
                        continue
					}

                    if run.status.Running {
                        if file != nil {
        					run.status.AddResult(file)

        					if err := run.runWriters(file); err != nil {
        						logger.Error("failed to write result for file", "err", err)
        					}
                        }else{
                            run.AddSkipped()
                        }
                    }

				}
			}

		}()
	}

	wg.Wait()
	run.status.Running = false
	swg.Wait()

    //fmt.Fprintf(os.Stderr, "\n%s\n%s\r", 
    //    "                                                                                ",
    //    "                                                                                ",
    //)

	return *run.status
}

func (run *Runner) Close() {
	// close the driver
	run.Parser.Close()
    ascii.ClearLine()
    ascii.ShowCursor()
}

// DetectBytes scans the given bytes and returns a list of findings
func (run *Runner) DetectBytes(content []byte) []models.Finding {
    return run.DetectString(string(content))
}

// DetectString scans the given string and returns a list of findings
func (run *Runner) DetectString(content string) []models.Finding {
    return run.Detect(Fragment{
        Raw: content,
    })
}

// Detect scans the given fragment and returns a list of findings
func (run *Runner) Detect(fragment Fragment) []models.Finding {
    var findings []models.Finding

    // add newline indices for location calculation in detectRule
    fragment.newlineIndices = newLineRegexp.FindAllStringIndex(fragment.Raw, -1)

    // setup variables to handle different decoding passes
    currentRaw := fragment.Raw
    encodedSegments := []EncodedSegment{}
    currentDecodeDepth := 0
    decoder := NewDecoder()

    for {
        // build keyword map for prefiltering rules
        keywords := make(map[string]bool)
        normalizedRaw := strings.ToLower(currentRaw)
        matches := run.prefilter.MatchString(normalizedRaw)
        for _, m := range matches {
            keywords[normalizedRaw[m.Pos():int(m.Pos())+len(m.Match())]] = true
        }

        for _, rule := range run.Identifiers.Rules {
            if len(rule.Keywords) == 0 {
                // if no keywords are associated with the rule always scan the
                // fragment using the rule
                findings = append(findings, run.detectRule(fragment, currentRaw, rule, encodedSegments)...)
                continue
            }

            // check if keywords are in the fragment
            for _, k := range rule.Keywords {
                if _, ok := keywords[strings.ToLower(k)]; ok {
                    findings = append(findings, run.detectRule(fragment, currentRaw, rule, encodedSegments)...)
                    break
                }
            }
        }

        // increment the depth by 1 as we start our decoding pass
        currentDecodeDepth++

        // stop the loop if we've hit our max decoding depth
        if currentDecodeDepth > run.MaxDecodeDepth {
            break
        }

        // decode the currentRaw for the next pass
        currentRaw, encodedSegments = decoder.decode(currentRaw, encodedSegments)

        // stop the loop when there's nothing else to decode
        if len(encodedSegments) == 0 {
            break
        }
    }

    return findings
}

// DetectReader accepts an io.Reader and a buffer size for the reader in KB
func (run *Runner) DetectReader(r io.Reader, bufSize int) ([]models.Finding, error) {
    reader := bufio.NewReader(r)
    buf := make([]byte, 1000*bufSize)
    findings := []models.Finding{}

    for {
        n, err := reader.Read(buf)

        // "Callers should always process the n > 0 bytes returned before considering the error err."
        // https://pkg.go.dev/io#Reader
        if n > 0 {
            // Try to split chunks across large areas of whitespace, if possible.
            peekBuf := bytes.NewBuffer(buf[:n])
            if readErr := readUntilSafeBoundary(reader, n, maxPeekSize, peekBuf); readErr != nil {
                return findings, readErr
            }

            fragment := Fragment{
                Raw: peekBuf.String(),
            }
            for _, finding := range run.Detect(fragment) {
                findings = append(findings, finding)
            }
        }

        if err != nil {
            if err == io.EOF {
                break
            }
            return findings, err
        }
    }

    return findings, nil
}

func (run *Runner) DetectFile(file *models.File) error {
    logger := run.log.With("path", file.FilePath)
    logger.Debug("Scanning path")

    f, err := os.Open(file.FilePath)
    if err != nil {
        if os.IsPermission(err) {
            logger.Warn("Skipping file: permission denied")
            return err
        }
        return err
    }
    defer func() {
        _ = f.Close()
    }()

    // Get file size
    fileInfo, err := f.Stat()
    if err != nil {
        return err
    }
    fileSize := fileInfo.Size()
    if run.MaxTargetMegaBytes > 0 {
        rawLength := fileSize / 1000000
        if rawLength > int64(run.MaxTargetMegaBytes) {
            logger.Debug("Skipping file: exceeds --max-target-megabytes", "size", rawLength)
            return errors.New("Skipping file: exceeds --max-target-megabytes")
        }
    }

    var (
        // Buffer to hold file chunks
        reader     = bufio.NewReaderSize(f, chunkSize)
        buf        = make([]byte, chunkSize)
        totalLines = 0
        resultMutex sync.Mutex
    )
    for {
        n, err := reader.Read(buf)

        // "Callers should always process the n > 0 bytes returned before considering the error err."
        // https://pkg.go.dev/io#Reader
        if n > 0 {
            // Only check the filetype at the start of file.
            if totalLines == 0 {
                // TODO: could other optimizations be introduced here?
                if mimetype, err := filetype.Match(buf[:n]); err != nil {
                    return err
                } else if mimetype.MIME.Type == "application" {
                    return errors.New(fmt.Sprintf("Cannot parse %s files", mimetype.MIME.Value)) // skip binary files
                }
            }

            // Try to split chunks across large areas of whitespace, if possible.
            peekBuf := bytes.NewBuffer(buf[:n])
            if readErr := readUntilSafeBoundary(reader, n, maxPeekSize, peekBuf); readErr != nil {
                return readErr
            }

            // Count the number of newlines in this chunk
            chunk := peekBuf.String()
            linesInChunk := strings.Count(chunk, "\n")
            totalLines += linesInChunk
            fragment := Fragment{
                Raw:      chunk,
                Bytes:    peekBuf.Bytes(),
                FilePath: file.FilePath,
            }
            for _, finding := range run.Detect(fragment) {
                if !run.status.Running {
                    return nil
                }

                // need to add 1 since line counting starts at 1
                finding.StartLine += (totalLines - linesInChunk) + 1
                finding.EndLine += (totalLines - linesInChunk) + 1
                resultMutex.Lock()

                if finding.Credential.Username != "" {
                    run.status.Credential += 1
                    finding.Credential.Time = file.Date
                    finding.Credential.Rule = finding.RuleID
                    file.Credentials = append(file.Credentials, finding.Credential)
                }

                if finding.Email.Email != "" {
                    run.status.Email += 1
                    finding.Email.Time = file.Date
                    file.Emails = append(file.Emails, finding.Email)
                }

                if finding.Url.Url != "" {
                    run.status.Url += 1
                    finding.Url.Time = file.Date
                    file.URLs = append(file.URLs, finding.Url)
                }

                resultMutex.Unlock()

            }
        }

        if err != nil {
            if err == io.EOF {
                return nil
            }
            return err
        }
    }
    
    return nil
}

func isWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

// shannonEntropy calculates the entropy of data using the formula defined here:
// https://en.wiktionary.org/wiki/Shannon_entropy
// Another way to think about what this is doing is calculating the number of bits
// needed to on average encode the data. So, the higher the entropy, the more random the data, the
// more bits needed to encode that data.
func shannonEntropy(data string) (entropy float64) {
	if data == "" {
		return 0
	}

	charCounts := make(map[rune]int)
	for _, char := range data {
		charCounts[char]++
	}

	invLength := 1.0 / float64(len(data))
	for _, count := range charCounts {
		freq := float64(count) * invLength
		entropy -= freq * math.Log2(freq)
	}

	return entropy
}

// readUntilSafeBoundary consumes |f| until it finds two consecutive `\n` characters, up to |maxPeekSize|.
// This hopefully avoids splitting. (https://github.com/gitleaks/gitleaks/issues/1651)
func readUntilSafeBoundary(r *bufio.Reader, n int, maxPeekSize int, peekBuf *bytes.Buffer) error {
    if peekBuf.Len() == 0 {
        return nil
    }

    // Does the buffer end in consecutive newlines?
    var (
        data         = peekBuf.Bytes()
        lastChar     = data[len(data)-1]
        newlineCount = 0 // Tracks consecutive newlines
    )
    if isWhitespace(lastChar) {
        for i := len(data) - 1; i >= 0; i-- {
            lastChar = data[i]
            if lastChar == '\n' {
                newlineCount++

                // Stop if two consecutive newlines are found
                if newlineCount >= 2 {
                    return nil
                }
            } else if lastChar == '\r' || lastChar == ' ' || lastChar == '\t' {
                // The presence of other whitespace characters (`\r`, ` `, `\t`) shouldn't reset the count.
                // (Intentionally do nothing.)
            } else {
                break
            }
        }
    }

    // If not, read ahead until we (hopefully) find some.
    newlineCount = 0
    for {
        data = peekBuf.Bytes()
        // Check if the last character is a newline.
        lastChar = data[len(data)-1]
        if lastChar == '\n' {
            newlineCount++

            // Stop if two consecutive newlines are found
            if newlineCount >= 2 {
                break
            }
        } else if lastChar == '\r' || lastChar == ' ' || lastChar == '\t' {
            // The presence of other whitespace characters (`\r`, ` `, `\t`) shouldn't reset the count.
            // (Intentionally do nothing.)
        } else {
            newlineCount = 0 // Reset if a non-newline character is found
        }

        // Stop growing the buffer if it reaches maxSize
        if (peekBuf.Len() - n) >= maxPeekSize {
            break
        }

        // Read additional data into a temporary buffer
        b, err := r.ReadByte()
        if err != nil {
            if err == io.EOF {
                break
            }
            return err
        }
        peekBuf.WriteByte(b)
    }
    return nil
}

func ContainsStopWord(s string) (bool, string) {
	s = strings.ToLower(s)
	for _, stopWord := range rules.DefaultStopWords {
		if strings.Contains(s, strings.ToLower(stopWord)) {
			return true, stopWord
		}
	}
	return false, ""
}

func ContainsEmailDomainStopWord(s string) (bool, string) {
    s = strings.ToLower(s)
    for _, stopWord := range rules.EmailDomainStopWords {
        if strings.Contains(s, strings.ToLower(stopWord)) {
            return true, stopWord
        }
    }
    return false, ""
}


func ContainsUrlDomainStopWord(s string) (bool, string) {
    s = strings.ToLower(s)
    for _, stopWord := range rules.UrlDomainStopWords {
        if strings.Contains(s, strings.ToLower(stopWord)) {
            return true, stopWord
        }
    }
    return false, ""
}


// detectRule scans the given fragment for the given rule and returns a list of findings
func (run *Runner) detectRule(fragment Fragment, currentRaw string, r *rules.Rule, encodedSegments []EncodedSegment) []models.Finding {
	var (
		findings []models.Finding
		logger   = run.log.With("rule", r.RuleID)
	)

    if !run.status.Running {
        return findings
    }

	if r.Path != nil && r.Regex == nil && len(encodedSegments) == 0 {
		// Path _only_ rule
		if r.Path.MatchString(fragment.FilePath) {
			finding := models.Finding{
				Description: r.Description,
				File:        fragment.FilePath,
				SymlinkFile: fragment.SymlinkFile,
				RuleID:      r.RuleID,
				Match:       fmt.Sprintf("file detected: %s", fragment.FilePath),
				Tags:        r.Tags,
                Credential:  models.Credential{},
                Email:       models.Email{},
                Url:         models.URL{},
			}
			return append(findings, finding)
		}
	} else if r.Path != nil {
		// if path is set _and_ a regex is set, then we need to check both
		// so if the path does not match, then we should return early and not
		// consider the regex
		if !r.Path.MatchString(fragment.FilePath) {
			return findings
		}
	}

	// if path only rule, skip content checks
	if r.Regex == nil {
		return findings
	}

	// if flag configure and raw data size bigger then the flag
	if run.MaxTargetMegaBytes > 0 {
		rawLength := len(currentRaw) / 1000000
		if rawLength > run.MaxTargetMegaBytes {
			logger.Debug("skipping fragment: size", "size", rawLength, "max-size", run.MaxTargetMegaBytes)
			return findings
		}
	}

    // Replace the main encoding chars
    currentRaw = strings.Replace(currentRaw, "%40", "@", -1)
    currentRaw = strings.Replace(currentRaw, "%20", " ", -1)
    currentRaw = strings.Replace(currentRaw, "%22", "\"", -1)
    currentRaw = strings.Replace(currentRaw, "%27", "'", -1)
    currentRaw = strings.Replace(currentRaw, "%7b", "{", -1)
    currentRaw = strings.Replace(currentRaw, "%7d", "}", -1)
    currentRaw = strings.Replace(currentRaw, "%5b", "[", -1)
    currentRaw = strings.Replace(currentRaw, "%0a", "\n", -1)
    currentRaw = strings.Replace(currentRaw, "%0d", "\r", -1)
    currentRaw = strings.Replace(currentRaw, "%09", "\t", -1)
    currentRaw = strings.Replace(currentRaw, "<br />", "\n", -1)
    currentRaw = strings.Replace(currentRaw, "<br/>", "\n", -1)
    currentRaw = strings.Replace(currentRaw, "<br>", "\n", -1)

    
	// use currentRaw instead of fragment.Raw since this represents the current
	// decoding pass on the text
    //MatchLoop:

	for _, matchIndex := range r.Regex.FindAllStringIndex(currentRaw, -1) {
		// Extract secret from match
		secret := strings.Trim(currentRaw[matchIndex[0]:matchIndex[1]], "\n\r\t")

		// For any meta data from decoding
		var metaTags []string

		// Check if the decoded portions of the segment overlap with the match
		// to see if its potentially a new match
		if len(encodedSegments) > 0 {
			if segment := segmentWithDecodedOverlap(encodedSegments, matchIndex[0], matchIndex[1]); segment != nil {
				matchIndex = segment.adjustMatchIndex(matchIndex)
				metaTags = append(metaTags, segment.tags()...)
			} else {
				// This item has already been added to a finding
				continue
			}
		} else {
			// Fixes: https://github.com/gitleaks/gitleaks/issues/1352
			// removes the incorrectly following line that was detected by regex expression '\n'
			matchIndex[1] = matchIndex[0] + len(secret)
		}

        //Recheck RegExp Match
        //if !r.Regex.MatchString(secret) {
        //    continue
        //}

		// determine location of match. Note that the location
		// in the finding will be the line/column numbers of the _match_
		// not the _secret_, which will be different if the secretGroup
		// value is set for this rule
		loc := location(fragment, matchIndex)

		if matchIndex[1] > loc.endLineIndex {
			loc.endLineIndex = matchIndex[1]
		}

		finding := models.Finding{
			Description: r.Description,
			File:        fragment.FilePath,
			SymlinkFile: fragment.SymlinkFile,
			RuleID:      r.RuleID,
			StartLine:   loc.startLine,
			EndLine:     loc.endLine,
			StartColumn: loc.startColumn,
			EndColumn:   loc.endColumn,
			Secret:      secret,
			Match:       secret,
			Tags:        append(r.Tags, metaTags...),
			Line:        fragment.Raw[loc.startLineIndex:loc.endLineIndex],
		}

		// Set the value of |secret|, if the pattern contains at least one capture group.
		// (The first element is the full match, hence we check >= 2.)
		groups := r.Regex.FindStringSubmatch(finding.Secret)
		if len(groups) >= 2 {
			if r.SecretGroup > 0 {
				if len(groups) <= r.SecretGroup {
					// Config validation should prevent this
					continue
				}
				finding.Secret = groups[r.SecretGroup]
			} else {
				// If |secretGroup| is not set, we will use the first suitable capture group.
				for _, s := range groups[1:] {
					if len(s) > 0 {
						finding.Secret = s
						break
					}
				}
			}
		}

        if len(r.Keywords) > 0 {
            ok := false
            // check if keywords are in the Math
            for _, k := range r.Keywords {
                if kok := strings.Contains(finding.Match, k); kok {
                    ok = true
                }
            }

            if !ok {
                //logger.Debug("skipping finding: keywords not found", "finding", finding.Secret)
                continue
            }
        }

		// check entropy
		entropy := shannonEntropy(finding.Secret)
		finding.Entropy = float32(entropy)
		if r.Entropy != 0.0 {
			// entropy is too low, skip this finding
			if entropy <= r.Entropy {
				logger.Debug("skipping finding: low entropy", "match", finding.Match, "secret", finding.Secret, "entropy", finding.Entropy)
				continue
			}
		}
		
		if r.CheckGlobalStopWord {
			if ok, word := ContainsStopWord(finding.Secret); ok {
				logger.Debug("skipping finding: global allowlist stopword", "match", finding.Match, "secret", finding.Secret, "allowed-stopword", word)
				continue
			}
		}

        //Process final finding
        ok, err := r.PostProcessor(&finding); 
        if err != nil {
            logger.Debug("post-processing error", "finding", finding.Secret, "err", err)
            continue
        }
        if !ok { //Just ignore
            continue
        }

        //If all is OK, get near text

        nearIndexStart := loc.startLineIndex - run.options.Parser.NearTextSize
        if nearIndexStart < 0 {
            nearIndexStart = 0
        }
        nearIndexEnd := loc.endLineIndex + run.options.Parser.NearTextSize
        if nearIndexEnd <= nearIndexStart {
            nearIndexEnd += nearIndexStart + 1
        }
        if nearIndexEnd > len(fragment.Raw) {
            nearIndexEnd = len(fragment.Raw)
        }

        nearText := fragment.Raw[nearIndexStart:nearIndexEnd]

        if finding.Credential.Username != "" {
            finding.Credential.NearText = nearText
            if finding.Credential.Url == "" {
                //Check Domain deny list
                if ok, _ := ContainsEmailDomainStopWord(finding.Credential.UserDomain); ok {  
                    finding.Credential.Username = ""
                }
            }
        }

        if finding.Email.Email != "" {
            finding.Email.NearText = nearText
            if ok, _ := ContainsEmailDomainStopWord(finding.Email.Domain); ok {   
                finding.Email.Email = ""
            }
        }

        if finding.Url.Url != "" {
            finding.Url.NearText = nearText
            if ok, _ := ContainsUrlDomainStopWord(finding.Url.Domain); ok { 
                finding.Url.Url = ""
            }
        }

        if finding.Credential.Username == "" && finding.Email.Email == "" && finding.Url.Url == "" {
            continue
        }

		findings = append(findings, finding)
	}
	return findings
}

func allTrue(bools []bool) bool {
	allMatch := true
	for _, check := range bools {
		if !check {
			allMatch = false
			break
		}
	}
	return allMatch
}
