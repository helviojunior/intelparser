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

    "time"
    "path/filepath"
    "github.com/gofrs/uuid"
)

// IntelligentSearchRequest is the information from the human for the search.
type IntelligentSearchRequest struct {
    Term        string        `json:"term"`       // Search term submitted by the user, e.g. "Document 1.docx" or "email@example.com"
    Buckets     []string      `json:"buckets"`    // Bucket identifiers
    Timeout     time.Duration `json:"timeout"`    // Timeout in seconds. May be limited by API config. 0 means default.
    MaxResults  int           `json:"maxresults"` // Total number of max results per bucket. May be limited by API config. 0 means default.
    DateFrom    string        `json:"datefrom"`   // Date from, both from/to are required if set, format "2006-01-02 15:04"
    DateTo      string        `json:"dateto"`     // Date to, both from/to are required if set, format "2006-01-02 15:04"
    Sort        int           `json:"sort"`       // Sort order: 0 = no sorting, 1 = X-Score ASC, 2 = X-Score DESC, 3 = Date ASC, 4 = Date DESC
    Media       int           `json:"media"`      // Media: 0 = not defined, otherwise MediaX as defined in ixservice
    TerminateID []uuid.UUID   `json:"terminate"`  // Optional: Previous search IDs to terminate (normal search or Phonebook). This is if the user makes a new search from the same tab. Same as first calling /intelligent/search/terminate.
}

// IntelligentSearchResponse is the result to the initial search request
type IntelligentSearchResponse struct {
    ID                  uuid.UUID `json:"id"`                  // id of the search job. This is used to get the results.
    SoftSelectorWarning bool      `json:"softselectorwarning"` // Warning of soft selectors, typically garbage in which results into garbage out
    Status              int       `json:"status"`              // Status of the search: 0 = Success (ID valid), 1 = Invalid Term, 2 = Error Max Concurrent Searches
}

// Tag classifies the items data
type Tag struct {
    ID    uint   `json:"id" gorm:"primarykey"`
    ItemID   uint `json:"item_id";gorm:"uniqueIndex:idx_tag_class_v"`
    Class int16  `json:"class";gorm:"uniqueIndex:idx_tag_class_v"` // Class of tag
    Value string `json:"value";gorm:"uniqueIndex:idx_tag_class_v"` // The value
}

func (Tag) TableName() string {
    return "intex_tag"
}

// Relationship defines a relation between 2 items.
type Relationship struct {
    ID    uint   `json:"id" gorm:"primarykey"`
    ItemID   uint `json:"item_id";gorm:"uniqueIndex:idx_relation_target_r"`
    Target   string `json:"target";gorm:"uniqueIndex:idx_relation_target_r"`   // Target item systemid
    Relation int    `json:"relation";gorm:"uniqueIndex:idx_relation_target_r"` // The relationship, see RelationX
}

func (Relationship) TableName() string {
    return "intex_relationship"
}

// Item represents any items meta-data. It origins from Indexed and is sent as search results.
// All fields except the identifier are optional and may be zero. It is perfectly valid that a service only knows partial information (like a name or storage id) of a given item.
type Item struct {
    ID          uint      `json:"id" gorm:"primarykey"`
    SystemID    string    `json:"systemid";gorm:"unique;not null";gorm:"index:idx_system_id`    // System identifier uniquely identifying the item
    StorageID   string    `json:"storageid"`   // Storage identifier, empty if not stored/available, otherwise a 64-byte blake2b hash hex-encoded
    InStore     bool      `json:"instore"`     // Whether the data of the item is in store and the storage id is valid. Also used to indicate update when false but storage id is set.
    Size        int64     `json:"size"`        // Size in bytes of the item data
    AccessLevel int       `json:"accesslevel"` // Native access level of the item (0 = Public..)
    Type        int       `json:"type"`        // Low-level content type (0 = Binary..)
    Media       int       `json:"media"`       // High-level media type (User, Paste, Tweet, Forum Post..)
    Added       time.Time `json:"added"`       // When the item was added to the system
    Date        time.Time `json:"date"`        // Full time stamp item when it was discovered or created
    Name        string    `json:"name"`        // Name or title
    Description string    `json:"description"` // Full description, text only
    XScore      int       `json:"xscore"`      // X-Score, ranking its relevancy. 0-100, default 50
    Simhash     uint64    `json:"simhash"`     // Simhash, depending on content type. Use hamming distance to compare equality of items data.
    Bucket      string    `json:"bucket"`      // Bucket
    Filename    string    `json:"filename"`    // FileName
    Downloaded  bool      `json:"downloaded"`  // If file already downloaded 

    // Tags are meta-data tags helping in classification of the items data. They reveal for example the language or a topic. Different to key-values they have hard-coded classes that
    // allow anyone to take action on them.
    Tags []Tag `json:"tags"`

    // Relations lists all related items.
    Relations []Relationship `json:"relations"`
}

func (Item) TableName() string {
    return "intex_item"
}

func (item *Item) GetExtension() string {

    if item.Filename != "" {
        ext := filepath.Ext(item.Filename)
        if ext != "" {
            return ext
        }
    }

    switch item.Media {
        case 9:
            return ".html"
        case 15:
            return ".pdf"
        case 16:
            return ".docx"
        case 17:
            return ".xlsx"
        case 18:
            return ".pptx"
        case 19:
            return ".png"
        case 20:
            return ".mp3"
        case 21:
            return ".mp4"
        case 22:
            return ".zip"
        case 23:
            return ".html"
        case 32:
            return ".csv"
        default:
            return ".txt"
    }
}

// PanelSearchResultTag represents a tag in human form.
type PanelSearchResultTag struct {
    ID    uint   `json:"id" gorm:"primarykey"`
    SearchResultID   uint `json:"search_result_id"`
    Class  int16  `json:"class"`  // Class of tag
    ClassH string `json:"classh"` // Class of tag, human friendly
    Value  string `json:"value"`  // The value
    ValueH string `json:"valueh"` // Value, human friendly
}

func (PanelSearchResultTag) TableName() string {
    return "intex_panel_result_tag"
}

// SearchResult represents a single result record. The entire record IS the de-facto result. Every field is optional and may be empty.
type SearchResult struct {
    Item
    AccessLevelH string                 `json:"accesslevelh"` // Human friendly access level info
    MediaH       string                 `json:"mediah"`       // Human friendly media type info
    SimhashH     string                 `json:"simhashh"`     // Human friendly simhash
    TypeH        string                 `json:"typeh"`        // Human friendly content type info
    TagsH        []PanelSearchResultTag `json:"tagsh"`        // Human friendly tags
    RandomID     string                 `json:"randomid"`     // Random ID
    BucketH      string                 `json:"bucketh"`      // Human friendly bucket name
    Group        string                 `json:"group"`        // File Group
    IndexFile    string                 `json:"indexfile"`    // Index file ID
}

func (SearchResult) TableName() string {
    return "intex_result_item"
}

func (sr *SearchResult) GetCsv() *CsvItem {
    return &CsvItem{
        Name        : sr.Name,
        Date        : sr.Date.Format(time.RFC3339),
        Bucket      : sr.BucketH,
        Media       : sr.MediaH,
        Content     : sr.TypeH,
        Type        : sr.TypeH,
        Size        : sr.Size,
        SystemID    : sr.SystemID,
    }
}


// IntelligentSearchResult contains the result items
type IntelligentSearchResult struct {
    Records []SearchResult `json:"records"` // The result records
    Status  int            `json:"status"`  // Status: 0 = Success with results, 1 = No more results available, 2 = Search ID not found, 3 = No results yet available keep trying
}

type CsvItem struct {
    Name    string         `json:"name"`
    Date    string         `json:"date"`
    Bucket  string         `json:"bucket"`
    Media   string         `json:"media"`
    Content string         `json:"content"` 
    Type    string         `json:"type"`
    Size    int64          `json:"size"` 
    SystemID string        `json:"system id"`
}
