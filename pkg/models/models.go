package models

import (
	"time"
	"encoding/json"
	//"fmt"
	"strings"
	
)

//Name,Date,Bucket,Media,Content Type,Size,System ID
type File struct {
	ID uint `json:"id" gorm:"primarykey"`

	Provider              string    `json:"provider"`  //IntelX, ...
	FilePath              string    `json:"file_path"`
	FileName              string    `json:"file_name"`
	Name                  string    `json:"name"`
	Date                  time.Time `json:"date"`
	Bucket                string    `json:"bucket"`
	MediaType             string    `json:"media_type"`
	IndexedAt             time.Time `json:"indexed_at"`

	Size		       	  uint   	`json:"size"`
	ProviderId	    	  string   	`json:"provider_id"`
	MIMEType    		  string    `json:"mime_type"`
	Fingerprint	    	  string   	`json:"fingerprint";gorm:"unique;not null"`

	// Failed flag set if the result should be considered failed
	Failed       		  bool   	`json:"failed"`
	FailedReason 		  string 	`json:"failed_reason"`

	Credentials []Credential `json:"credentials" gorm:"constraint:OnDelete:CASCADE"`
	Emails      []Email      `json:"emails" gorm:"constraint:OnDelete:CASCADE"`
	URLs        []URL        `json:"urls" gorm:"constraint:OnDelete:CASCADE"`

}


type URL struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id"`

	Time        time.Time   `json:"time"`

	Domain		string      `json:"domain"`
	Url         string      `json:"url"`

	NearText    string 		`json:"next_text"`
}

type Email struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id"`

	Time        time.Time   `json:"time"`

	Domain		string      `json:"domain"`
	Email       string      `json:"email"`

	NearText    string 		`json:"next_text"`
}

type Credential struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id"`

	Rule        string      `json:"rule"`
	Time        time.Time   `json:"time"`

	UserDomain	string      `json:"user_domain"`
	Username    string      `json:"username"`
	Password    string      `json:"password"`

	Url         string      `json:"url"`
	UrlDomain	string      `json:"url_domain"`

	Severity    int 	    `json:"severity"`
	Entropy     float32     `json:"entropy"`

	NearText    string 		`json:"next_text"`
}

// Finding contains information about strings that
// have been captured by a tree-sitter query.
type Finding struct {
    // Rule is the name of the rule that was matched
    RuleID      string
    Description string

    StartLine   int
    EndLine     int
    StartColumn int
    EndColumn   int

    Line string `json:"-"`

    Match string

    // Secret contains the full content of what is matched in
    // the tree-sitter query.
    Secret string

    // File is the name of the file containing the finding
    File        string
    SymlinkFile string
    Commit      string
    Link        string `json:",omitempty"`

    // Entropy is the shannon entropy of Value
    Entropy float32

    Author  string
    Date    string
    Message string
    Tags    []string

    // unique identifier
    Fingerprint string

    Credential Credential
    Email Email
    Url URL
}

/* Custom Marshaller for File */
func (file File) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Provider              string    `json:"provider"`
		FilePath              string    `json:"file_path"`
		FileName              string    `json:"file_name"`
		Name                  string    `json:"name"`
		LeakDate              string    `json:"leak_date"`
		Bucket                string    `json:"bucket"`
		MediaType             string    `json:"media_type"`
		IndexedAt             string    `json:"indexed_at"`

		Size		       	  uint   	`json:"size"`
		ProviderId	    	  string   	`json:"provider_id"`
		MIMEType    		  string    `json:"mime_type"`
		Fingerprint	    	  string   	`json:"fingerprint""`

	}{
		Provider 			: file.Provider,
		FilePath 			: file.FilePath,
		FileName 			: file.FileName,
		Name 				: file.Name,
		LeakDate    		: file.Date.Format(time.RFC3339),
		Bucket 				: file.Bucket,
		MediaType 			: file.MediaType,
		IndexedAt 			: file.IndexedAt.Format(time.RFC3339),
		Size 				: file.Size,
		ProviderId 			: file.ProviderId,
		MIMEType 			: file.MIMEType,
		Fingerprint			: file.Fingerprint,
	})
}


/* Custom Marshaller for Credential */
func (cred Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Rule                  string    `json:"rule"`
		Time 	              string    `json:"time"`
		UserDomain 	    	  string   	`json:"user_domain"`
		Username    		  string    `json:"username"`
		Password	    	  string   	`json:"password""`
		Url 		    	  string   	`json:"url""`
		UrlDomain			  string    `json:"url_domain"`
		Severity	    	  int   	`json:"severity""`
		Entropy  	    	  float32  	`json:"entropy""`
		NearText	    	  string   	`json:"near_text""`

	}{
		Rule 				: cred.Rule,
		Time 	    		: cred.Time.Format(time.RFC3339),
		UserDomain			: strings.ToLower(cred.UserDomain),
		Username 			: cred.Username,
		Password 			: cred.Password,
		Url 				: cred.Url,
		UrlDomain			: strings.ToLower(cred.UrlDomain),
		Severity 			: cred.Severity,
		Entropy 			: cred.Entropy,
		NearText 			: cred.NearText,
	})
}


/* Custom Marshaller for URL */
func (u URL) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Time 	              string    `json:"time"`
		Domain   	    	  string   	`json:"domain"`
		Url 		    	  string   	`json:"url""`
		NearText	    	  string   	`json:"near_text""`

	}{
		Time 	    		: u.Time.Format(time.RFC3339),
		Domain 				: strings.ToLower(u.Domain),
		Url 				: u.Url,
		NearText 			: u.NearText,
	})
}

/* Custom Marshaller for URL */
func (eml Email) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Time 	              string    `json:"time"`
		Domain   	    	  string   	`json:"domain"`
		Email 		    	  string   	`json:"email""`
		NearText	    	  string   	`json:"near_text""`

	}{
		Time 	    		: eml.Time.Format(time.RFC3339),
		Domain 				: strings.ToLower(eml.Domain),
		Email 				: strings.ToLower(eml.Email),
		NearText 			: eml.NearText,
	})
}

