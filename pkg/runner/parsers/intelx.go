package driver

import (
	//"bytes"
	//"context"
	//"encoding/base64"
	//"errors"
	//"fmt"
	//"image"
	"bufio"
	"log/slog"
	"encoding/csv"
	"os"
	//"os/exec"
	//"path/filepath"
	"strings"
	//"sync"
	"path/filepath"
	"time"
	"strconv"
	"slices"
	"sync"

	"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/models"
	"github.com/helviojunior/intelparser/pkg/runner"
	"github.com/helviojunior/intelparser/pkg/database"
	"gorm.io/gorm"
)

var MustSaveBucketWords = []string{
	"leaks",
}

var MustSaveNameWords = []string{
	"passwords.txt",
	"history.txt",
	"brute.txt",
	"autofills",
}

type InfoData struct {
	Name string
	Date time.Time
	Bucket string
	Media string
	Content string 
	Type string
	Size uint 
	SystemID string
}

// Chromedp is a driver that probes web targets using chromedp
// Implementation ref: https://github.com/chromedp/examples/blob/master/multi/main.go
type IntelxParser struct {
	// options for the Runner to consider
	options runner.Options
	// logger
	log *slog.Logger
	//Info data
	info []InfoData
	//
	conn *gorm.DB
	//
	infoMutex sync.Mutex
}

// NewChromedp returns a new Chromedp instance
func NewInteX(logger *slog.Logger, opts runner.Options) (*IntelxParser, error) {
	var conn *gorm.DB
	var err error
	conn, err = database.Connection(opts.Writer.GlobalDbURI, true, false)
	if err != nil {
		logger.Debug("Error connecting to the database", "conn", opts.Writer.GlobalDbURI, "err", err)
		conn = nil
	}

	return &IntelxParser{
		options: opts,
		log:     logger,
		info: 	 []InfoData{},
		conn:    conn,
		infoMutex:   sync.Mutex{},
	}, nil
}

// witness does the work of probing a url.
// This is where everything comes together as far as the runner is concerned.
func (run *IntelxParser) ParseFile(thisRunner *runner.Runner, file runner.FileItem) (*models.File, error) {
	logger := run.log.With("file_path", file.RealPath)
	var err error

	file_name_ext := filepath.Base(file.RealPath)
	var (
		result = &models.File{
			Provider: "IntelX",
			FilePath: file.RealPath,
			FileName: file_name_ext,
			Name: file.VirtualPath,
			Date: time.Now(),
			IndexedAt: time.Now(),
		}
	)

	if strings.ToLower(file_name_ext) == "info.csv" {
		err = run.ParseInfo(file.RealPath)
		return result, err
	}

	file_name := strings.ToLower(strings.TrimSuffix(file_name_ext, filepath.Ext(file_name_ext)))
	result.Fingerprint, _ = tools.GetHashFromFile(file.RealPath)
	result.MIMEType, _ = tools.GetMimeType(file.RealPath)

	if run.conn != nil {
		response := run.conn.Raw("SELECT count(id) as count from files WHERE failed = 0 AND file_name = ? AND fingerprint = ?", file_name_ext, result.Fingerprint)
	    if response != nil {
	        var cnt int
	        _ = response.Row().Scan(&cnt)
	        if cnt > 0 {
	            logger.Debug("[File already parsed]")
	            return nil, nil
	        }
	    }
	}

	idx := slices.IndexFunc(run.info, func(i InfoData) bool { return i.SystemID == file_name })
	logger.Debug("Get info", "info_idx", idx)

	if idx == -1 {
		if !tools.SliceHasStr([]string{".DS_Store"}, file_name_ext) {
			logger.Warn("File is not present at info.csv, ignoring...")
		}else{
			logger.Debug("File is not present at info.csv, ignoring...")
		}
	    return nil, nil
	}

	if idx >= 0 {
		info := run.info[idx]
		result.Name = info.Name
		result.Date = info.Date
		result.Bucket = "IntelX » " + info.Bucket
		result.MediaType = info.Content
		result.Size = info.Size
		result.ProviderId = info.SystemID
		result.Date = info.Date
		logger.Debug("Get info", "info_data", info)
	}

	logger = run.log.With("file", file_name_ext)
	logger.Debug("Parsing file")

	if err := thisRunner.DetectFile(result); err != nil {
		return result, err
	}

	//Check if we must save the file content
	if run.MustSaveContent(result) { //&& result.MIMEType == "text/plain" {
		logger.Debug("saving file content")
		result.Content, _ = tools.ReadTextFile(result.FilePath)
	}

	result.FilePath = file.VirtualPath

	return result, nil
}

func (run *IntelxParser) MustSaveContent(file *models.File) bool {
    s := strings.ToLower(file.Bucket)
    n := strings.ToLower(file.Name)

	for _, bucketWord := range MustSaveBucketWords {
		if strings.Contains(s, strings.ToLower(bucketWord)) {
		    for _, nameWord := range MustSaveNameWords {
		        if strings.Contains(n, strings.ToLower(nameWord)) {
		            return true
		        }
		    }
        }
    }

    return false
}

func (run *IntelxParser) Close() {
	run.log.Debug("closing IntelX parser context")
}

func GetOrDefault(data []string, index int, def string) string {
	if index == -1 {
		return def
	}

	return strings.Trim(string(data[index]), " \r\n\t")
}

func (run *IntelxParser) ParseInfo(file_path string) error {
	f, err := os.Open(file_path)
    if err != nil {
        return err
    }
    defer f.Close()

    br := bufio.NewReader(f)
    r, _, err := br.ReadRune()
    if err != nil {
        return err
    }
    if r != '\uFEFF' {
        br.UnreadRune() // Not a BOM -- put the rune back
    }

    csvReader := csv.NewReader(br)
    records, err := csvReader.ReadAll()
    if err != nil {
        return err
    }

    Name := -1
	Date := -1
	Bucket := -1
	Media := -1
	Content := -1
	Type := -1
	Size := -1
	SystemID := -1

	for idx, c := range records[0] {
		switch strings.ToLower(c) {
		    case "name":
		        Name = idx
		    case "date":
		        Date = idx
		    case "bucket":
		        Bucket = idx
		    case "media":
		        Media = idx
		    case "content":
		        Content = idx
		    case "type":
		        Type = idx
		    case "size":
		        Size = idx
		    case "system id":
		        SystemID = idx
		    
	    }
	}

	run.infoMutex.Lock()
	defer run.infoMutex.Unlock()

	for idx, rec := range records {
		if idx > 0 {
			s, err := strconv.Atoi(rec[Size])
			if err != nil {
				s = 0
			}

			dt, err := time.Parse("2006-01-02 15:04:05", GetOrDefault(rec, Date, ""))
			if err != nil {
				dt, err = time.Parse(time.RFC3339, GetOrDefault(rec, Date, ""))
				if err != nil {
					dt = time.Now()
				}
			}

			run.info = append(run.info, InfoData{
                            Name:  		GetOrDefault(rec, Name, ""),
                            Date:  		dt,
                            Bucket:  	GetOrDefault(rec, Bucket, ""),
                            Media:  	GetOrDefault(rec, Media, ""),
                            Content:  	GetOrDefault(rec, Content, ""),
                            Type:  		GetOrDefault(rec, Type, ""),
                            Size:  		uint(s),
                            SystemID:  	strings.ToLower(GetOrDefault(rec, SystemID, "")),
                        })
		}
	}

    return nil
}