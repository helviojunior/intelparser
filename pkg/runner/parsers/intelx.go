package driver

import (
	//"bytes"
	//"context"
	//"encoding/base64"
	//"errors"
	//"fmt"
	//"image"
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

	"github.com/helviojunior/intelparser/internal/islazy"
	"github.com/helviojunior/intelparser/pkg/models"
	"github.com/helviojunior/intelparser/pkg/runner"
	"github.com/helviojunior/intelparser/pkg/database"
	"gorm.io/gorm"
)

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
}

// NewChromedp returns a new Chromedp instance
func NewInteX(logger *slog.Logger, opts runner.Options) (*IntelxParser, error) {

	conn, err := database.Connection("sqlite:///" + opts.Writer.UserPath +"/.intelparser.db", true, false)
	if err != nil {
		return nil, err
	}

	return &IntelxParser{
		options: opts,
		log:     logger,
		info: 	 []InfoData{},
		conn:    conn,
	}, nil
}

// witness does the work of probing a url.
// This is where everything comes together as far as the runner is concerned.
func (run *IntelxParser) ParseFile(thisRunner *runner.Runner, file_path string) (*models.File, error) {
	logger := run.log.With("file_path", file_path)
	var err error

	file_name_ext := filepath.Base(file_path)
	var (
		result = &models.File{
			Provider: "IntelX",
			FilePath: file_path,
			FileName: file_name_ext,
			Name: file_path,
			Date: time.Now(),
			IndexedAt: time.Now(),
		}
	)

	if file_name_ext == "Info.csv" {
		err = run.ParseInfo(file_path)
		return result, err
	}

	file_name := strings.TrimSuffix(file_name_ext, filepath.Ext(file_name_ext))
	result.Fingerprint, _ = islazy.GetHashFromFile(file_path)
	result.MIMEType, _ = islazy.GetMimeType(file_path)

	response := run.conn.Raw("SELECT count(id) as count from files WHERE failed = 0 AND file_name = ? AND fingerprint = ?", file_name_ext, result.Fingerprint)
    if response != nil {
        var cnt int
        _ = response.Row().Scan(&cnt)
        if cnt > 0 {
            logger.Debug("[File already parsed]")
            return nil, nil
        }
    }

	idx := slices.IndexFunc(run.info, func(i InfoData) bool { return i.SystemID == file_name })
	if idx >= 0 {
		info := run.info[idx]
		result.Name = info.Name
		result.Date = info.Date
		result.Bucket = info.Bucket
		result.MediaType = info.Content
		result.Size = info.Size
		result.ProviderId = info.SystemID
		result.Date = info.Date

	}
	logger = run.log.With("file", file_name_ext)
	logger.Debug("Parsing file")

	if err := thisRunner.DetectFile(result); err != nil {
		return result, err
	}

	return result, nil
}

func (run *IntelxParser) Close() {
	run.log.Debug("closing IntelX parser context")
}

func (run *IntelxParser) ParseInfo(file_path string) error {
	f, err := os.Open(file_path)
    if err != nil {
        return err
    }
    defer f.Close()

    csvReader := csv.NewReader(f)
    records, err := csvReader.ReadAll()
    if err != nil {
        return err
    }

    Name := 0
	Date := 0
	Bucket := 0
	Media := 0
	Content := 0
	Type := 0
	Size := 0
	SystemID := 0

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

	for idx, rec := range records {
		if idx > 0 {
			s, err := strconv.Atoi(rec[Size])
			if err != nil {
				s = 0
			}

			dt, err := time.Parse("2006-01-02 15:04:05", rec[Date])
			if err != nil {
				dt = time.Now()
			}

			run.info = append(run.info, InfoData{
                            Name:  		rec[Name],
                            Date:  		dt,
                            Bucket:  	rec[Bucket],
                            Media:  	rec[Media],
                            Content:  	rec[Content],
                            Type:  		rec[Type],
                            Size:  		uint(s),
                            SystemID:  	rec[SystemID],
                        })
		}
	}



    return nil
}