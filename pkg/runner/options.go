package runner

import (
    "time"
)

// Options are global github.com/helviojunior/intelparserintelparser options
type Options struct {
    // Logging is logging options
    Logging Logging
    // Chrome is Chrome related options
    Writer Writer
    // Parser is typically Parser options
    Parser Parser

    // Force use current path as Temp Path
    StoreLocalWorkspace bool

    // Default DB as Control Only (ignore other tables)
    DefaultDBAsControlOnly bool

    DateFilter *time.Time

    IndexedDateFilter *time.Time
}

// Logging is log related options
type Logging struct {
    // Debug display debug level logging
    Debug bool
    // LogScanErrors log errors related to scanning
    LogScanErrors bool
    // Silence all logging
    Silence bool
    //Text file output
    TextFile string
}

// Writer options
type Writer struct {
    UserPath  string
    WorkingPath  string
    NoControlDb bool
    GlobalDbURI string
    Db        bool
    DbURI     string
    DbDebug   bool // enables verbose database logs
    Csv       bool
    CsvFile   string
    Jsonl     bool
    JsonlFile string
    ELastic   bool
    ELasticURI string
    Stdout    bool
    None      bool
}

// Scan is scanning related options
type Parser struct {
    // Path/file to be parsed
    Path string
    // Threads (not really) are the number of goroutines to use.
    // More soecifically, its the go-rod page pool well use.
    Threads int
    //Size of near text data
    NearTextSize int

    StoreNearText bool
}

// NewDefaultOptions returns Options with some default values
func NewDefaultOptions() *Options {
    return &Options{
        Parser: Parser{
            Path:             "invalid",
            Threads:          6,
            NearTextSize:     50,
            StoreNearText:    false,
        },
        Logging: Logging{
            Debug:         true,
            LogScanErrors: true,
        },
        
    }
}