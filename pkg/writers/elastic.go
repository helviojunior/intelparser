package writers

import (
	"encoding/json"
	"time"
	"net/url"
	"math"
	"fmt"
	"strings"
	"context"
	"errors"
	"bytes"
	"net/http"
	"crypto/tls"
	//"reflect"
	"io"
	"os"
    "strconv"

	"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/models"
	elk "github.com/elastic/go-elasticsearch/v8"
	esapi "github.com/elastic/go-elasticsearch/v8/esapi"
	logger "github.com/helviojunior/intelparser/pkg/log"
)

// fields in the main model to ignore
var elkExludedFields = []string{"failed", "failed_reason", "near_text"}
var elkBulkCount = 200
var elkBulkMaxSize = 5 * 1024 * 1024

// JsonWriter is a JSON lines writer
type ElasticWriter struct {
	Client *elk.Client
	Index string
}

type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []struct {
		Index struct {
			ID     string `json:"_id"`
			Result string `json:"result"`
			Status int    `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
				Cause  struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"caused_by"`
			} `json:"error"`
		} `json:"index"`
	} `json:"items"`
}

type indexResponse struct {
	ID     string `json:"_id"`
	Index  string `json:"_index"`
	Result string `json:"result"`
	Error  struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
		Cause  struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"caused_by"`
	} `json:"error"`
}

type Interceptor struct {
  base   	*http.Transport
}


func (i Interceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	// Header exigido pelo client do ES
	const prodHeaderKey = "X-Elastic-Product"
	const prodHeaderVal = "Elasticsearch"

	// O client do ES costuma checar GET /
	if (req.Method == http.MethodGet || req.Method == http.MethodHead) && req.URL.Path == "/" {
		str_body := ""
		if req.Method != http.MethodHead {
		
			str_body = `{
			  "version": { "number": "8.0.0-SNAPSHOT", "build_flavor": "default" },
			  "tagline": "You Know, for Search"
			}`
		}

		resp := &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(str_body)),
			Header:     make(http.Header),
			Request:    req,
		}
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set(prodHeaderKey, prodHeaderVal)
		return resp, nil
	}

	resp, err := i.base.RoundTrip(req)
	if resp != nil {
		resp.Header.Set(prodHeaderKey, prodHeaderVal)
	}
	return resp, err
}

// NewJsonWriter return a new Json lines writer
func NewElasticWriter(uri string) (*ElasticWriter, error) {

	u, err := url.Parse(uri)
	if err != nil {
	    return nil, err
	}

	username := u.User.Username()
	password, _ := u.User.Password()
	port := u.Port()
	if port == "" {
		port = "9200"
	}
	index_name := u.EscapedPath()
	index_name = strings.Trim(index_name, "/ ")
	index_name = strings.SplitN(index_name, "/", 2)[0]
	if index_name == "" {
		index_name = "intelparser"
	}

	wr := &ElasticWriter{
		Index: index_name,
	}

	conf := elk.Config{
	    Addresses: []string{
            fmt.Sprintf("%s://%s:%s/", u.Scheme, u.Hostname(), port),
        },
        //Username: username,
        //Password: password,
        //CACert:   cert,
		RetryOnStatus: []int{429, 502, 503, 504},
		RetryBackoff:  func(i int) time.Duration {
			// A simple exponential delay
			d := time.Duration(math.Exp2(float64(i))) * time.Second
			logger.Debugf("Elastic retry, attempt: %d | Sleeping for %s...\n", i, d)
			return d
		},
		Transport: &Interceptor{
			&http.Transport{
				MaxIdleConns:       10,
			    IdleConnTimeout:    10 * time.Second,
			    DisableCompression: true,
			    TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	// Check username and password from Environment Variables
	if v1, ok := os.LookupEnv("INTELPARSER_OUTPUT_USERNAME"); ok {
		conf.Username = v1;
	}
	if v1, ok := os.LookupEnv("INTELPARSER_OUTPUT_PASSWORD"); ok {
		conf.Password = v1;
	}

	if username != "" && password != "" {
		conf.Username = username
		conf.Password = password
	}

	wr.Client, err = elk.NewClient(conf)
	if err != nil {
	    return nil, err
	}

	// Faz um ping (chama GET / internamente)
	res, err := wr.Client.Ping()
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if v1, ok := os.LookupEnv("ELK_BULK_SIZE"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 > 10 {
				logger.Infof("Setting maximum ELK bulk count as %d using env.ELK_BULK_SIZE", i1)
				elkBulkCount = int(i1)
			}  
		}
	}

	if v1, ok := os.LookupEnv("ELK_BULK_BYTES"); ok {
		if i1, err := strconv.ParseInt(v1, 10, 32); err == nil {
			if i1 > 4094 {
				logger.Infof("Setting maximum ELK bulk size as %s using env.ELK_BULK_BYTES", tools.Bytes(uint64(i1)))
				elkBulkMaxSize = int(i1)
			}  
		}
	}

	//File Index
	err = wr.CreateIndex(wr.Index, `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "indexed_at": {"type": "date"},
                    "leak_date": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "name": {"type": "keyword"},
                    "file_name": {"type": "text"},
                    "file_path": {"type": "keyword"},
                    "mime_type": {"type": "keyword"},
                    "size": {"type": "long"},
                    "provider": {"type": "keyword"},
                    "provider_id": {"type": "text"},
                    "bucket": {"type": "text"},
                    "media_type": {"type": "text"},
                    "content": {"type": "text"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	//Credential Index
	err = wr.CreateIndex(wr.Index + "_creds", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "rule": {"type": "keyword"},
                    "user_domain": {"type": "keyword"},
                    "username": {"type": "keyword"},
                    "password": {"type": "keyword"},
                    "cpf": {"type": "keyword"},
                    "url": {"type": "keyword"},
                    "url_domain": {"type": "keyword"},
                    "severity": {"type": "long"},
                    "entropy": {"type": "long"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	//Urls Index
	err = wr.CreateIndex(wr.Index + "_urls", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "domain": {"type": "keyword"},
                    "url": {"type": "keyword"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}


	//Emails Index
	err = wr.CreateIndex(wr.Index + "_emails", `{
		    "settings": {
                    "number_of_replicas": 1,
                    "index": {"highlight.max_analyzed_offset": 10000000}
                },

            "mappings": {
                "properties": {
                    "time": {"type": "date"},
                    "fingerprint": {"type": "keyword"},
                    "domain": {"type": "keyword"},
                    "email": {"type": "keyword"},
                    "near_text": {"type": "text"},
                    "bucket": {"type": "text"},
                    "file_id": {"type": "keyword"}
                }
            }
		}`)
	if err != nil {
	    return nil, err
	}

	return wr, nil
}

// Write JSON lines to a file
func (ew *ElasticWriter) Write(result *models.File) error {
	var err error

    logger.Debugf("Integrating elastic: %d credentials, %d e-mails, %d urls", 
    	len(result.Credentials),  len(result.Emails), len(result.URLs))

	docs := make(map[string][]byte)
	docs_len := 0

	//Credentials
	for _, c := range result.Credentials {
		b_data, err := json.Marshal(c)
		if err != nil {
		    return err
		}

		//cid := tools.GetHash(b_data)
		cid := c.CalcHash(result.Fingerprint)
		b_data, err = ew.MarshalAppend(b_data, map[string]interface{}{
			"file_id": result.Fingerprint,
			"bucket": result.Bucket,
			"fingerprint": cid,
		})
		if err != nil {
		    return err
		}

		//err = ew.CreateDoc(ew.Index + "_creds", b_data, cid)
		//if err != nil {
		//    return err
		//}

		docs[cid] = b_data
		docs_len += len(b_data)

		if len(docs) >= elkBulkCount || docs_len >= elkBulkMaxSize {
			err = ew.CreateDocBulk(ew.Index + "_creds", docs)
			if err != nil {
			    return err
			}
			docs = make(map[string][]byte)
			docs_len = 0
		}
	}
	if len(docs) > 0 {
		err = ew.CreateDocBulk(ew.Index + "_creds", docs)
		if err != nil {
		    return err
		}
	}

	//Urls
	docs = make(map[string][]byte)
	for _, u := range result.URLs {
		b_data, err := json.Marshal(u)
		if err != nil {
		    return err
		}

		cid := u.CalcHash(result.Fingerprint)
		b_data, err = ew.MarshalAppend(b_data, map[string]interface{}{
			"file_id": result.Fingerprint,
			"bucket": result.Bucket,
			"fingerprint": cid,
		})
		if err != nil {
		    return err
		}

		//err = ew.CreateDoc(ew.Index + "_urls", b_data, cid)
		//if err != nil {
		//    return err
		//}

		docs[cid] = b_data
		docs_len += len(b_data)

		if len(docs) >= elkBulkCount || docs_len >= elkBulkMaxSize {
			err = ew.CreateDocBulk(ew.Index + "_urls", docs)
			if err != nil {
			    return err
			}
			docs = make(map[string][]byte)
			docs_len = 0
		}
	}
	if len(docs) > 0 {
		err = ew.CreateDocBulk(ew.Index + "_urls", docs)
		if err != nil {
		    return err
		}
	}

	//Emails
	docs = make(map[string][]byte)
	docs_len = 0
	for _, eml := range result.Emails {
		b_data, err := json.Marshal(eml)
		if err != nil {
		    return err
		}

		cid := eml.CalcHash(result.Fingerprint)
		b_data, err = ew.MarshalAppend(b_data, map[string]interface{}{
			"file_id": result.Fingerprint,
			"bucket": result.Bucket,
			"fingerprint": cid,
		})
		if err != nil {
		    return err
		}

		//err = ew.CreateDoc(ew.Index + "_emails", b_data, cid)
		//if err != nil {
		//    return err
		//}

		docs[cid] = b_data
		docs_len += len(b_data)

		if len(docs) >= elkBulkCount || docs_len >= elkBulkMaxSize {
			err = ew.CreateDocBulk(ew.Index + "_emails", docs)
			if err != nil {
			    return err
			}
			docs = make(map[string][]byte)
			docs_len = 0
		}
	}
	if len(docs) > 0 {
		err = ew.CreateDocBulk(ew.Index + "_emails", docs)
		if err != nil {
		    return err
		}
	}

    //File
    result.Credentials = []models.Credential{}
    result.Emails = []models.Email{}
    result.URLs = []models.URL{}

	b_data, err := json.Marshal(*result) //ew.Marshal(*result)
	if err != nil {
	    return err
	}

	res, err := ew.Client.Index(ew.Index, bytes.NewReader(b_data), ew.Client.Index.WithDocumentID(result.Fingerprint))
	if err != nil {
	    return err
	}
	if res.StatusCode != 200 && res.StatusCode != 201 {
		fmt.Printf("Err: %s", res)
		return errors.New("Cannot create/update document")
	}

	return nil
}

func (ew *ElasticWriter) CreateIndex(index string, mapping string) error {

	var raw map[string]interface{}

	response, err := ew.Client.Indices.Exists([]string{index})
	if err != nil {
	    return err
	}
	defer response.Body.Close()

    if response.IsError() {

		if response.StatusCode == 404 {
			indexReq := esapi.IndicesCreateRequest{
			    Index: index,
			    Body: strings.NewReader(string(mapping)),
			}

			logger.Infof("Creating elastic index %s", index)
			res, err := indexReq.Do(context.Background(), ew.Client)
			if err != nil {
			    return err
			}
			defer res.Body.Close()

			if res.IsError() {

		        if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		            return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
		        } else {
		            return errors.New(fmt.Sprintf("Cannot create/update elastic index [%d] %s: %s",
		                res.StatusCode,
		                raw["error"].(map[string]interface{})["type"],
		                raw["error"].(map[string]interface{})["reason"],
		            ))
		        }

			}

		}else{

	        if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
	            return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
	        } else {
	            return errors.New(fmt.Sprintf("Cannot get elastic index [%d] %s: %s",
	                response.StatusCode,
	                raw["error"].(map[string]interface{})["type"],
	                raw["error"].(map[string]interface{})["reason"],
	            ))
	        }


		}

    }

    return nil

}

func (ew *ElasticWriter) CreateDocBulk(index string, docs map[string][]byte) error {
    var raw map[string]interface{}
    var buf bytes.Buffer
    size := 0
    for id, doc := range docs {
    	meta := []byte(fmt.Sprintf(`{ "index" : { "_id" : "%s" } }%s`, id, "\n"))
    	data := []byte(doc)
    	data = append(data, "\n"...)

    	size += len(meta) + len(data)
    	buf.Grow(len(meta) + len(data))
		buf.Write(meta)
		buf.Write(data)

    }

    logger.Debugf("Elastic bulk %d docs, %d bytes", len(docs), size)

    for i := range 10 {

        res, err := ew.Client.Bulk(bytes.NewReader(buf.Bytes()), ew.Client.Bulk.WithIndex(index))
        if err != nil {
            return err
        }
        defer res.Body.Close()

        if res.IsError() {

            if i >= 5 {
                if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
                    return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
                } else {
                    return errors.New(fmt.Sprintf("Error: [%d] %s: %s",
                        res.StatusCode,
                        raw["error"].(map[string]interface{})["type"],
                        raw["error"].(map[string]interface{})["reason"],
                    ))
                }

            }

            // A successful response might still contain errors for particular documents...
            //
        } else {
        	var blk *bulkResponse
            if err := json.NewDecoder(res.Body).Decode(&blk); err != nil {
                return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
            } else {
                for _, d := range blk.Items {
                    // ... so for any HTTP status above 201 ...
                    //
                    if d.Index.Status > 201 {
                        // ... and print the response status and error information ...
                        logger.Debugf("  Error: [%d]: %s: %s: %s: %s",
                            d.Index.Status,
                            d.Index.Error.Type,
                            d.Index.Error.Reason,
                            d.Index.Error.Cause.Type,
                            d.Index.Error.Cause.Reason,
                        )
                    } else {
                        // ... otherwise increase the success counter.
                        //
                        
                    }
                }
            }
        }

        if res.StatusCode == 200 || res.StatusCode == 201 {
            return nil
        }

        time.Sleep(1 * time.Second)
    }

    return errors.New("Cannot create/update document")
}


func (ew *ElasticWriter) CreateDoc(index string, data []byte, doc_id string) error {
	var raw map[string]interface{}
	for i := range 10 {
		res, err := ew.Client.Index(index, bytes.NewReader(data), ew.Client.Index.WithDocumentID(doc_id))
		if err != nil {
		    return err
		}
		defer res.Body.Close()

		if res.IsError() {

			if i >= 5 {
				if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
					return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
				} else {
					return errors.New(fmt.Sprintf("Error: [%d] %s: %s",
						res.StatusCode,
						raw["error"].(map[string]interface{})["type"],
						raw["error"].(map[string]interface{})["reason"],
					))
				}

			}

			// A successful response might still contain errors for particular documents...
			//
		} else {

			if res.StatusCode == 200 || res.StatusCode == 201 {
				return nil
			}

			//bodyBytes, err := io.ReadAll(res.Body)
		    //if err != nil {
		    //    return err
		    //}
		    //bodyString := string(bodyBytes)
			//fmt.Printf("Resp: %s", bodyString)

			var idxRes *indexResponse
			
			if err := json.NewDecoder(res.Body).Decode(&idxRes); err != nil {
				return errors.New(fmt.Sprintf("Failure to to parse response body: %s", err))
			} else {
				//Debug result
			}
		}

		time.Sleep(1 * time.Second)
	}

	return errors.New("Cannot create/update document")
}



func (ew *ElasticWriter) MarshalAppend(marshalled []byte, new_data map[string]interface{}) ([]byte, error) {
	t_data := make(map[string]interface{})
	err := json.Unmarshal(marshalled, &t_data)

	data := make(map[string]interface{})
	for k, v := range t_data {
		// skip excluded fields
		if tools.SliceHasStr(elkExludedFields, k) {
			continue
		}

		data[k] = v
    }

    for k, v := range new_data {
    	data[k] = v
    }

	j_data, err := json.Marshal(data)
	if err != nil {
		return []byte{}, err
	}

	return j_data, nil
}


func (ew *ElasticWriter) Marshal(v any) ([]byte, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return []byte{}, err
	}

	t_data := make(map[string]interface{})
	err = json.Unmarshal(j, &t_data)

	data := make(map[string]interface{})
	for k, v := range t_data {
		// skip excluded fields
		if tools.SliceHasStr(elkExludedFields, k) {
			continue
		}

		data[k] = v
    }

	j_data, err := json.Marshal(data)
	if err != nil {
		return []byte{}, err
	}

	return j_data[:], nil
}
