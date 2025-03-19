/*
File Name:  API.go
Copyright:  2018 Kleissner Investments s.r.o.
Author:     Peter Kleissner
Version:    1 from 11/19/2018

API client code for using the Intelligence X API. Create an IntelligenceXAPI object and call Init first.
You must set your API key.
*/

package ixapi

import (
    "context"
    "crypto/tls"
    "errors"
    "io"
    "io/ioutil"
    "net"
    "net/http"
    "net/url"
    "strconv"
    "strings"
    "time"
    "os"
    //"fmt"

    "github.com/gofrs/uuid"
    "github.com/helviojunior/intelparser/pkg/log"

)

const defaultAPIURL = "https://2.intelx.io/"
const publicAPIKey = "00000000-0000-0000-0000-000000000000"

// Sort orders
const (
    SortNone       = 0 // No sorting
    SortXScoreAsc  = 1 // X-Score ascending = Least relevant first
    SortXScoreDesc = 2 // X-Score descending = Most relevant first
    SortDateAsc    = 3 // Date ascending = Oldest first
    SortDateDesc   = 4 // Date descending = Newest first
)


// IntelligenceXAPI holds all information for communicating with the Intelligence X API.
// Call Init() first.
type IntelligenceXAPI struct {
    URL string    // The API URL. Always ending with slash.
    Key uuid.UUID // The API key assigned by Intelligence X. Contact the company to receive one.

    // additional input. Set before calling Init
    ProxyURL string // Proxy to use
    BindToIP string // Bind to a specific IPv4 or IPv6

    // below are the HTTP client settings

    // one client for the session
    Client              http.Client
    RetryAttempts       int // in case of underlying transport failure
    UserAgent           string
    HTTPMaxResponseSize int64

    HTTPTimeout time.Duration

    WriteCounter *WriteCounter
}

// Init initializes the IX API. URL and Key may be empty to use defaults.
func (api *IntelligenceXAPI) Init(URL string, Key string) {
    api.SetAPIKey(URL, Key)

    api.RetryAttempts = 1
    api.HTTPMaxResponseSize = 1000 * 1024 * 1024 // 1000 MB
    api.WriteCounter = &WriteCounter{}

    // Timeouts
    NetworkDialerTimeout := 10 * time.Second
    NetworkTLSTimeout := 10 * time.Second
    HTTPTimeout := 60 * time.Second
    IdleConnTimeout := 90 * time.Second
    KeepAlive := 30 * time.Second

    // Check if to bind on a specific IP. Warning, IPv4 is not available when binding on IPv6! The reverse is true as well.
    var localAddr *net.TCPAddr
    if api.BindToIP != "" {
        localAddr = &net.TCPAddr{
            IP: net.ParseIP(api.BindToIP),
        }
    }

    // create the HTTP client
    var ProxyURLParsed *url.URL
    if api.ProxyURL != "" {
        ProxyURLParsed, _ = url.Parse(api.ProxyURL)
    }

    transport := &http.Transport{
        Proxy: http.ProxyURL(ProxyURLParsed),
        Dial: (&net.Dialer{
            LocalAddr: localAddr,
            Timeout:   NetworkDialerTimeout,
            KeepAlive: KeepAlive,
        }).Dial,
        TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
        TLSHandshakeTimeout: NetworkTLSTimeout,
        MaxIdleConns:        0,
        MaxIdleConnsPerHost: 100,
        IdleConnTimeout:     IdleConnTimeout,
        DisableKeepAlives:   false,
    }

    api.Client = http.Client{
        Transport: transport,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            // Prevent implicit redirection on client.Do calls so that no requests without appropriate headers are sent
            return http.ErrUseLastResponse
        },
        Timeout: HTTPTimeout,
    }

    api.HTTPTimeout = HTTPTimeout
}

// WriteCounter counts the number of bytes written to it. It implements to the io.Writer interface
// and we can pass this into io.TeeReader() which will report progress on each write cycle.
type WriteCounter struct {
    Total uint64
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
    n := len(p)
    wc.Total += uint64(n)
    return n, nil
}

// SetAPIKey sets the API URL and Key. URL and Key may be empty to use defaults.
func (api *IntelligenceXAPI) SetAPIKey(URL string, Key string) {
    if URL == "" {
        URL = defaultAPIURL
    }
    if Key == "" {
        eKey := os.Getenv("IXAPIKEY")
        if eKey != "" {
            Key = eKey
        }else {
            Key = publicAPIKey
        }
    }

    if !strings.HasSuffix(URL, "/") {
        URL += "/"
    }

    api.URL = URL
    api.Key, _ = uuid.FromString(Key)
}

// SearchStart starts a search
func (api *IntelligenceXAPI) SearchStart(ctx context.Context, Term string) (searchID uuid.UUID, selectorInvalid bool, err error) {
    request := IntelligentSearchRequest{Term: Term, Sort: SortDateDesc}
    response := IntelligentSearchResponse{}

    if err = api.httpRequestPost(ctx, "intelligent/search", request, &response); err != nil {
        return
    }

    switch response.Status {
    case 1:
        return searchID, false, errors.New("Invalid Term")
    case 2:
        return searchID, false, errors.New("Error Max Concurrent Searches")
    }

    return response.ID, response.SoftSelectorWarning, nil
}

// SearchStartAdvanced starts a search and allows the caller to set any advanced filter
func (api *IntelligenceXAPI) SearchStartAdvanced(ctx context.Context, Input IntelligentSearchRequest) (searchID uuid.UUID, selectorInvalid bool, err error) {
    response := IntelligentSearchResponse{}

    if err = api.httpRequestPost(ctx, "intelligent/search", Input, &response); err != nil {
        return
    }

    switch response.Status {
    case 1:
        return searchID, false, errors.New("Invalid Term")
    case 2:
        return searchID, false, errors.New("Error Max Concurrent Searches")
    }

    return response.ID, response.SoftSelectorWarning, nil
}

// SearchGetResults returns results
// Status: 0 = Success with results (continue), 1 = No more results available (this response might still have results), 2 = Search ID not found, 3 = No results yet available keep trying, 4 = Error
func (api *IntelligenceXAPI) SearchGetResults(ctx context.Context, searchID uuid.UUID, Limit int) (records []SearchResult, status int, err error) {
    request := "?id=" + searchID.String() + "&limit=" + strconv.Itoa(Limit) + "&previewlines=20"
    response := IntelligentSearchResult{}

    if err = api.httpRequestGet(ctx, "intelligent/search/result"+request, &response); err != nil {
        return nil, 4, err
    }

    return response.Records, response.Status, nil
}

// SearchTerminate terminates a search
func (api *IntelligenceXAPI) SearchTerminate(ctx context.Context, searchID uuid.UUID) (err error) {
    request := "?id=" + searchID.String()

    return api.httpRequestGet2(ctx, "intelligent/search/terminate"+request)
}

// FilePreview loads the preview of an item. Previews are always capped at 1000 characters.
func (api *IntelligenceXAPI) FilePreview(ctx context.Context, item *Item) (text string, err error) {
    // Request: GET /file/preview?c=[Content Type]&m=[Media Type]&f=[Target Format]&sid=[Storage Identifier]&b=[Bucket]&e=[0|1]
    request := "?sid=" + item.StorageID + "&f=0&l=20&c=" + strconv.Itoa(item.Type) + "&m=" + strconv.Itoa(item.Media) + "&b=" + item.Bucket + "&k=" + api.Key.String()

    response, err := api.httpRequest(ctx, "file/preview"+request, "GET", nil, "")
    if err != nil {
        return "", err
    }

    defer response.Body.Close()

    if response.StatusCode != http.StatusOK {
        return "", api.apiStatusToError(response.StatusCode)
    }

    responseBytes, err := ioutil.ReadAll(io.LimitReader(response.Body, 1000))

    return string(responseBytes), err
}

// DownloadZip reads the data of an item.
func (api *IntelligenceXAPI) DownloadZip(ctx context.Context, searchID uuid.UUID, Limit int, OutputFile string) (err error) {
    // Request: GET /intelligent/search/export?id=[search id]&f=1&l=[limit]&k=[api key]
    request := "?f=1&l=" + strconv.Itoa(Limit) + "&id=" + searchID.String() + "&k=" + api.Key.String()

    // Create the file, but give it a tmp file extension, this means we won't overwrite a
    // file until it's downloaded, but we'll remove the tmp extension once downloaded.
    out, err := os.Create(OutputFile + ".tmp")
    if err != nil {
        return err
    }

    api.Client.Timeout = 0
    response, err := api.httpRequest2("intelligent/search/export"+request, "GET", nil, "")
    if err != nil {
        return err
    }

    defer response.Body.Close()

    if response.StatusCode != http.StatusOK {
        return api.apiStatusToError(response.StatusCode)
    }

    // Create our progress reporter and pass it to be used alongside our writer
    api.WriteCounter = &WriteCounter{}
    if _, err = io.Copy(out, io.TeeReader(response.Body, api.WriteCounter)); err != nil {
        out.Close()
        if errors.Is(err, context.DeadlineExceeded) {
            log.Debug("ContextDeadlineExceeded: true")
        }
        if os.IsTimeout(err) {
            log.Debug("IsTimeoutError: true")
        }

        return err
    }

    // Close the file without defer so it can happen before Rename()
    out.Close()
    
    if err = os.Rename(OutputFile+".tmp", OutputFile); err != nil {
        return err
    }
    return nil
}

// FileRead reads the data of an item.
func (api *IntelligenceXAPI) FileRead(ctx context.Context, item *Item, Limit int64) (data []byte, err error) {
    // Request: GET /file/read?type=0&storageid=[storage identifier]&bucket=[optional bucket]
    request := "?type=0&storageid=" + item.StorageID + "&bucket=" + item.Bucket

    response, err := api.httpRequest(ctx, "file/read"+request, "GET", nil, "")
    if err != nil {
        return nil, err
    }

    defer response.Body.Close()

    if response.StatusCode != http.StatusOK {
        return nil, api.apiStatusToError(response.StatusCode)
    }

    responseBytes, err := ioutil.ReadAll(io.LimitReader(response.Body, Limit))

    return responseBytes, err
}

// SearchGetResultsAll returns all results up to Limit and up to the given Timeout. It will automatically terminate the search before returning.
// Unless the underlying API requests report and error, no error will be returned. Deadline exceeded is treated as no error.
func (api *IntelligenceXAPI) SearchGetResultsAll(ctx context.Context, searchID uuid.UUID, Limit int, Timeout time.Duration) (records []SearchResult, err error) {
    var lastStatus int

    newContext, cancel := context.WithDeadline(ctx, time.Now().Add(Timeout))
    defer cancel()

    for {
        var recordsNew []SearchResult
        currentLimit := Limit - len(records)
        recordsNew, lastStatus, err = api.SearchGetResults(newContext, searchID, currentLimit)

        if err != nil && (strings.Contains(err.Error(), context.Canceled.Error()) || strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
            lastStatus = 5
            break
        } else if err != nil {
            return records, err
        }

        if len(recordsNew) > 0 {
            records = append(records, recordsNew...)
        }

        if len(records) >= Limit {
            break
        }

        // Status: 0 = Success with results (continue), 1 = No more results available (this response might still have results), 2 = Search ID not found, 3 = No results yet available keep trying, 4 = Error
        if lastStatus != 0 && lastStatus != 3 {
            break
        }

        // wait 250 ms before querying the results again
        time.Sleep(time.Millisecond * 250)
    }

    // Terminate the search if required. When Status: 0 = Success with results (continue), 3 = No results yet available keep trying, 4 = Error, 5 = Deadline exceeded
    if lastStatus == 0 || lastStatus == 3 || lastStatus == 4 || lastStatus == 5 {
        //api.SearchTerminate(context.Background(), searchID)
    }

    if lastStatus != 4 {
        err = nil
    }

    return records, err
}