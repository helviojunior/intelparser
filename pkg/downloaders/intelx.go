package downloaders

import (

    //"errors"
    "context"
    //"fmt"
    "sync"
    "time"
    "path/filepath"
    "strings"
    "os"
    "archive/zip"
    "io"
    "encoding/csv"
    "fmt"
	"reflect"

    //"github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/islazy"
    "github.com/helviojunior/intelparser/pkg/database"
    "github.com/helviojunior/intelparser/pkg/ixapi"
    "github.com/helviojunior/intelparser/pkg/log"
    "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const textSupportedSelectors = "Selector types supported:\n* Email address\n* Domain, including wildcards like *.example.com\n* URL\n* IPv4 and IPv6\n* CIDRv4 and CIDRv6\n* Phone Number\n* Bitcoin address\n* MAC address\n* IPFS Hash\n* UUID\n* Simhash\n* Credit card number\n* IBAN\n"
var csvExludedFields = []string{"near_text"}

type IntelXDownloader struct {
	Term string
	ZipFile string
	Threads int
	ProxyURL string // Proxy to use

	apiKey string
	ctx    context.Context
	dbName string
	conn          *gorm.DB
	mutex         sync.Mutex
	tempFolder string

	status *IntelXDownloaderStatus

	results chan ixapi.SearchResult
}

type IntelXDownloaderStatus struct {
	TotalFiles int
	Downloaded int
	Duplicated int
	TotalBytes int64
	Label string
	Running bool
}

func NewIntelXDownloader(term string, apiKey string, outZipFile string) (*IntelXDownloader, error) {
    tempFolder, err := islazy.CreateDir(islazy.TempFileName("", "intelparser_", ""))
    if err != nil {
        return nil, err
    }

    dbName := filepath.Join(tempFolder, "info.sqlite3")
    log.Info("Creating info database", "path", dbName)
	c, err := database.Connection("sqlite:///"+ dbName, false, false)
	if err != nil {
		return nil, err
	}

	// run database migrations on the connection
	if err := c.AutoMigrate(
		&ixapi.Tag{},
		&ixapi.Relationship{},
		//&ixapi.Item{},
		&ixapi.SearchResult{},
		&ixapi.PanelSearchResultTag{},
	); err != nil {
		return nil, err
	}

	return &IntelXDownloader{
		Term:     	term,
		ZipFile:    outZipFile,
		Threads:    3,
		apiKey: 	apiKey,
		dbName: 	dbName,
		conn: 		c,
		tempFolder: tempFolder,
		ctx: 		context.Background(),
		mutex:      sync.Mutex{},
		results:    make(chan ixapi.SearchResult),
		status:     &IntelXDownloaderStatus{
			TotalFiles: 0,
			Downloaded: 0,
			TotalBytes: 0,
			Label: "[=====]",
			Running: true,
		},
	}, nil
}

func (dwn *IntelXDownloader) Run() *IntelXDownloaderStatus { 
	r := true
	for r {
		c, err := dwn.SearchNext()
		if err != nil {
			return dwn.status
		}

		if c == 0 {
			r = false
		}
	}

	if dwn.status.TotalFiles > 0 {

	    //Write info.csv
	    log.Info("Writting Info.csv")
	    err := dwn.WriteInfoCsv()
	    if err != nil {
	        log.Error("Error writting Info.csv", "err", err)
	        return dwn.status
	    }


		log.Info("Downloading files")

		c, err := database.Connection("sqlite:///"+ dwn.dbName, false, false)
		if err != nil {
			log.Error("Error reconnecting to database", "err", err)
			return dwn.status
		}
		dwn.conn = c

		wg := sync.WaitGroup{}
		dwn.status.Running = true
		dwn.mutex.Lock()
		rows, err := dwn.conn.Model(&ixapi.SearchResult{}).Rows()
		dwn.mutex.Unlock()
	    defer rows.Close()
	    
	    if err != nil {
	    	log.Error("Error getting file list", "err", err)
	        return dwn.status
	    }

		dwn.results = make(chan ixapi.SearchResult)
	    go func() {
	    	defer close(dwn.results)

		    dwn.status.TotalFiles = 0
		    dwn.status.Duplicated = 0

		    var item ixapi.SearchResult
		    for rows.Next() {
		    	dwn.status.TotalFiles++
		        dwn.conn.ScanRows(rows, &item)
		        dwn.results <- item
		    }
		}()

		// will spawn Parser.Theads number of "workers" as goroutines
		for w := 0; w < dwn.Threads; w++ {
			wg.Add(1)
		    go func() {
		        defer wg.Done()
		        
				api := ixapi.IntelligenceXAPI{
					ProxyURL: dwn.ProxyURL,
				}
				api.Init("", dwn.apiKey)

		        for dwn.status.Running {
					select {
					case <-dwn.ctx.Done():
						return
					case record, ok := <-dwn.results:
						if !ok || !dwn.status.Running {
							return
						}
						
						err := dwn.DownloadResult(&api, record)
						
						if err != nil {
							log.Error("Error downloading file", "did", record.SystemID, "err", err)
						}else{
							dwn.status.Downloaded++
						}
						
						//previewText, _ := api.FilePreview(ctx, &record.Item)
						//resultLink := frontendBaseURL + "?did=" + record.SystemID.String()

						//title := record.Name
						//if title == "" {
						//	title = "Untitled Document"
						//}

						//text += fmt.Sprintf(templateRecordPlain, n, record.Date.UTC().Format("2006-01-02 15:04"), title, previewHTMLToText(previewText), resultLink)

					}
				}

		    }()
		}

	    wg.Wait()
	   
	    //Compress   
	    log.Info("Compressing files")
	    log.Debug("Destination", "zip", dwn.ZipFile)

	    entries, err := os.ReadDir(dwn.tempFolder)
	    if err != nil {
	        log.Error("Error getting file list from temp folder", "err", err)
	        return dwn.status
	    }
	 
	 	archive, err := os.Create(dwn.ZipFile)
	    if err != nil {
	        log.Error("Error creating zip file", "err", err)
	        return dwn.status
	    }
	    defer archive.Close()
	    zipWriter := zip.NewWriter(archive)

	    for _, e := range entries {
	    	log.Debug("Compressing", "file", e.Name())
	        f1, err := os.Open(filepath.Join(dwn.tempFolder, e.Name()))
		    if err != nil {
		        log.Error("Error openning file", "file", e.Name(), "err", err)
		    }else{
			    defer f1.Close()

			    //if e.Name() != "Info.sqlite3" {

			    w1, err := zipWriter.Create(e.Name())
			    if err != nil {
			        log.Error("Error creatting file at Zip container", "file", e.Name(), "err", err)
			    }else{
				    if _, err := io.Copy(w1, f1); err != nil {
				        log.Error("Error copping file data to Zip container", "file", e.Name(), "err", err)
				    }
				}
				//}
			}

	    }
	    zipWriter.Close()

	}

    islazy.RemoveFolder(dwn.tempFolder)

	return dwn.status
}

func (dwn *IntelXDownloader) WriteInfoCsv() error {
	file, err := os.OpenFile(filepath.Join(dwn.tempFolder, "Info.csv"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	c, err := database.Connection("sqlite:///"+ dwn.dbName, false, false)
	if err != nil {
		log.Error("Error reconnecting to database", "err", err)
		return err
	}

	rows, err := c.Model(&ixapi.SearchResult{}).Rows()
    defer rows.Close()
    if err != nil {
        return err
    }

	writer := csv.NewWriter(file)
	defer writer.Flush()

	//Write header
	val := reflect.ValueOf(ixapi.CsvItem{})
	numField := val.NumField()

	var fieldNames []string
	for i := 0; i < numField; i++ {
		// skip excluded fields
		if islazy.SliceHasStr(csvExludedFields, val.Type().Field(i).Name) {
			continue
		}

		// skip slices
		if val.Field(i).Kind() == reflect.Slice {
			continue // Optionally skip slice fields, or handle them differently
		}

		name := val.Type().Field(i).Name
		switch strings.ToLower(name) {
		case "systemid":
			name = "System ID"
		}

		fieldNames = append(fieldNames, name)
	}

	if err := writer.Write(fieldNames); err != nil {
		return err
	}

	//Write content
	var item ixapi.SearchResult
    for rows.Next() {
        c.ScanRows(rows, &item)

        // get values from the item
		val := reflect.ValueOf(*item.GetCsv())
		numField := val.NumField()

		var values []string
		for i := 0; i < numField; i++ {
			// skip excluded fields
			if islazy.SliceHasStr(csvExludedFields, val.Type().Field(i).Name) {
				continue
			}

			// skip slices
			if val.Field(i).Kind() == reflect.Slice {
				continue // Optionally skip slice fields, or handle them differently
			}

			values = append(values, fmt.Sprintf("%v", val.Field(i).Interface()))
		}

		if err := writer.Write(values); err != nil {
			return err
		}
    }

    return nil
	
}

func (dwn *IntelXDownloader) DownloadResult(api *ixapi.IntelligenceXAPI, record ixapi.SearchResult) error {
	logger := log.With("did", record.SystemID)

	fileName := filepath.Join(dwn.tempFolder, islazy.SafeFileName(record.SystemID) + record.GetExtension())

	logger.Debug("Downloading", "bytes", record.Size, "path", fileName)
	fData, err := api.FileRead(context.Background(), &record.Item, record.Size)
	if err != nil {
		logger.Debug("Error downloading data", "err", err)
		return err 
	}
	dwn.status.TotalBytes += record.Size

	// Open the file in append mode, create it if it doesn't exist
	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Debug("Error openning file", "err", err)
		return err 
	}
	defer file.Close()

	// Append the JSON data as a new line
	if _, err := file.Write(fData); err != nil {
		logger.Debug("Error saving file data", "err", err)
		return err 
	}
	return nil
}

func (dwn *IntelXDownloader) SearchNext() (int, error) {
	var DateFrom, DateTo time.Time
	var inserted int

	DateFrom = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	DateTo = time.Now().UTC()

	wg := sync.WaitGroup{}
	logger := log.With("term", dwn.Term)

	log.Info("Starting IX Api")
	search := ixapi.IntelligenceXAPI{
		ProxyURL: dwn.ProxyURL,
	}

	search.Init("", dwn.apiKey)

	logger.Info("Quering IX Api")

	response := dwn.conn.Raw("SELECT min(`date`) as dt1 from intex_result_item")
    if response != nil {
        var mDate time.Time
        var tDate string
        err := response.Row().Scan(&tDate)
        if err == nil {
        	tDate = tDate[0:10]
        	mDate, err = time.Parse("2006-01-02", tDate)
        	if err == nil {
        		DateTo = mDate.AddDate(0, 0, 1)
        	}
        }
    }

    logger.Info("Search time", "DateFrom", DateFrom, "DateTo", DateTo)
	results, selectorInvalid, err := search.SearchWithDates(dwn.ctx, dwn.Term, DateFrom, DateTo, ixapi.SortDateDesc, 1000, ixapi.DefaultWaitSortTime, ixapi.DefaultTimeoutGetResults)

	if err != nil {
		logger.Error("Error querying results", "err", err)
		return 0, err
	} else if len(results) == 0 && selectorInvalid {
		logger.Error("Invalid input selector. Please specify a strong selector")
		log.Warn(textSupportedSelectors)
		return 0, err
	}

	dwn.status.TotalFiles += len(results)
	logger.Debug("Results", "qty", len(results))
	dwn.results = make(chan ixapi.SearchResult)
    go func() {
    	defer close(dwn.results)
		for _, record := range results {
			dwn.results <- record
		}
	}()

	// will spawn Parser.Theads number of "workers" as goroutines
	for w := 0; w < dwn.Threads; w++ {
		wg.Add(1)
	    go func() {
	        defer wg.Done()
	        
	        for dwn.status.Running {
				select {
				case <-dwn.ctx.Done():
					return
				case record, ok := <-dwn.results:
					if !ok || !dwn.status.Running {
						return
					}
					
					logger.Debug("Reg", "did", record.SystemID)

					i, _ := dwn.WriteDb(&record)

					if i {
						inserted++
					}else{
						dwn.status.Duplicated++
					}

					//previewText, _ := api.FilePreview(ctx, &record.Item)
					//resultLink := frontendBaseURL + "?did=" + record.SystemID.String()

					//title := record.Name
					//if title == "" {
					//	title = "Untitled Document"
					//}

					//text += fmt.Sprintf(templateRecordPlain, n, record.Date.UTC().Format("2006-01-02 15:04"), title, previewHTMLToText(previewText), resultLink)

				}
			}

	    }()
	}

    wg.Wait()

    return inserted, nil
}

// Write results to the database
func (dwn *IntelXDownloader) WriteDb(result *ixapi.SearchResult) (bool, error) {
	dwn.mutex.Lock()
	defer dwn.mutex.Unlock()

	result.Simhash = 0
	//result.Tags = []ixapi.Tag{}
	//result.Relations = []ixapi.Relationship{}

	cnt := -1
	response := dwn.conn.Raw("SELECT count(id) as qty from intex_result_item WHERE system_id = ?", result.SystemID)
    if response != nil {
        _ = response.Row().Scan(&cnt)
    }

	if _, ok := dwn.conn.Statement.Clauses["ON CONFLICT"]; !ok {
		dwn.conn = dwn.conn.Clauses(clause.OnConflict{UpdateAll: true})
	}
	return cnt == 0, dwn.conn.CreateInBatches(result, 50).Error
}

func (dwn *IntelXDownloader) Close() {
	
}