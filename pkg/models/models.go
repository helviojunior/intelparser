package models

import (
	"time"
	"encoding/json"
	"fmt"
	"strings"
	"crypto/sha1"
    "encoding/hex"

	"github.com/helviojunior/intelparser/internal/tools"
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

	Content 		  	  string 	`json:"content"`

	// Failed flag set if the result should be considered failed
	Failed       		  bool   	`json:"failed"`
	FailedReason 		  string 	`json:"failed_reason"`

	Credentials []Credential `json:"credentials" gorm:"constraint:OnDelete:CASCADE"`
	Emails      []Email      `json:"emails" gorm:"constraint:OnDelete:CASCADE"`
	URLs        []URL        `json:"urls" gorm:"constraint:OnDelete:CASCADE"`
	Phones      []Phone      `json:"phones" gorm:"constraint:OnDelete:CASCADE"`
	Documents   []Document   `json:"documents" gorm:"constraint:OnDelete:CASCADE"`

}


type URL struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id" gorm:"index:idx_url"`

	Time        time.Time   `json:"time"`

	Domain		string      `json:"domain"`
	Url         string      `json:"url"`

	NearText    string 		`json:"near_text"`
}

type Email struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id" gorm:"index:idx_email"`

	Time        time.Time   `json:"time"`

	Domain		string      `json:"domain"`
	Email       string      `json:"email"`

	NearText    string 		`json:"near_text"`
}

type Phone struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id" gorm:"index:idx_phone"`

	Time        time.Time   `json:"time"`

	Country		string      `json:"country"`
	Raw         string      `json:"raw"`
	Phone       string      `json:"phone"`

	Source      string      `json:"source"`
	FileName    string      `json:"file_name"`
	Line        string      `json:"line"`

	NearText    string 		`json:"near_text"`
}

type Document struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id" gorm:"index:idx_document"`

	Time        time.Time   `json:"time"`

	Raw         string      `json:"raw"`
	Number      string      `json:"number"`
	IsCPF       bool        `json:"is_cpf"`
	IsCNPJ      bool        `json:"is_cnpj"`

	Source      string      `json:"source"`
	FileName    string      `json:"file_name"`
	Line        string      `json:"line"`

	NearText    string 		`json:"near_text"`
}

type Credential struct {
	ID       uint `json:"id" gorm:"primarykey"`
	FileID   uint `json:"file_id" gorm:"index:idx_cred"`

	Rule        string      `json:"rule"`
	Time        time.Time   `json:"time"`

	UserDomain	string      `json:"user_domain"`
	Username    string      `json:"username"`
	Password    string      `json:"password"`

	CPF         string      `json:"cpf"`

	Url         string      `json:"url"`
	UrlDomain	string      `json:"url_domain"`

	Severity    int 	    `json:"severity"`
	Entropy     float32     `json:"entropy"`

	NearText    string 		`json:"near_text"`
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
    Phone Phone
    Document Document
}


func (file File) Clone() *File {
	return &File{
		Provider			: file.Provider,
		FilePath 			: file.FilePath,
		FileName 			: file.FileName,
		Name 				: file.Name,
		Date 				: file.Date,
		Bucket 				: file.Bucket,
		MediaType 			: file.MediaType,
		IndexedAt 			: file.IndexedAt,
		Size 				: file.Size,
		ProviderId 			: file.ProviderId,
		MIMEType 			: file.MIMEType,
		Fingerprint 		: file.Fingerprint,
		Content 			: file.Content,

		//Credentials 		: make([]Credential{}),
		//Emails 				: make([]Email{}),
		//URLs 				: make([]URL{}),
	}
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
		Fingerprint	    	  string   	`json:"fingerprint"`
		Content 			  string   	`json:"content,omitempty"`

	}{
		Provider 			: file.Provider,
		FilePath 			: file.FilePath,
		FileName 			: file.FileName,
		Name 				: strings.ToLower(file.Name),
		LeakDate    		: file.Date.Format(time.RFC3339),
		Bucket 				: file.Bucket,
		MediaType 			: file.MediaType,
		IndexedAt 			: file.IndexedAt.Format(time.RFC3339),
		Size 				: file.Size,
		ProviderId 			: file.ProviderId,
		MIMEType 			: file.MIMEType,
		Fingerprint			: file.Fingerprint,
		Content			 	: file.Content,
	})
}


/* Custom Marshaller for Credential */
func (cred Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Rule                  string    `json:"rule"`
		Time 	              string    `json:"time"`
		UserDomain 	    	  string   	`json:"user_domain,omitempty"`
		Username    		  string    `json:"username"`
		Password	    	  string   	`json:"password"`
		CPF         		  string    `json:"cpf,omitempty"`
		Url 		    	  string   	`json:"url,omitempty"`
		UrlDomain			  string    `json:"url_domain,omitempty"`
		Severity	    	  int   	`json:"severity"`
		Entropy  	    	  float32  	`json:"entropy"`
		NearText	    	  string   	`json:"near_text"`

	}{
		Rule 				: cred.Rule,
		Time 	    		: cred.Time.Format(time.RFC3339),
		UserDomain			: strings.ToLower(cred.UserDomain),
		Username 			: cred.Username,
		Password 			: cred.Password,
		CPF 				: cred.CPF,
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
		Url 		    	  string   	`json:"url"`
		NearText	    	  string   	`json:"near_text"`

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
		Email 		    	  string   	`json:"email"`
		NearText	    	  string   	`json:"near_text"`

	}{
		Time 	    		: eml.Time.Format(time.RFC3339),
		Domain 				: strings.ToLower(eml.Domain),
		Email 				: strings.ToLower(eml.Email),
		NearText 			: eml.NearText,
	})
}


/* Custom Marshaller for Phone */
func (p Phone) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Time 	              string    `json:"time"`
		Country   	    	  string   	`json:"country"`
		Raw 		    	  string   	`json:"raw"`
		Phone 		    	  string   	`json:"phone"`
		Source 		    	  string   	`json:"source"`
		FileName 	    	  string   	`json:"file_name"`
		Line 		    	  string   	`json:"line"`
		NearText	    	  string   	`json:"near_text"`

	}{
		Time 	    		: p.Time.Format(time.RFC3339),
		Country 			: p.Country,
		Raw 				: p.Raw,
		Phone 				: p.Phone,
		Source 				: p.Source,
		FileName 			: p.FileName,
		Line 				: p.Line,
		NearText 			: p.NearText,
	})
}

/* Custom Marshaller for Document */
func (d Document) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Time 	              string    `json:"time"`
		Raw 		    	  string   	`json:"raw"`
		Number 		    	  string   	`json:"number"`
		IsCPF   	    	  bool   	`json:"is_cpf"`
		IsCNPJ   	    	  bool   	`json:"is_cnpj"`
		Source 		    	  string   	`json:"source"`
		FileName 	    	  string   	`json:"file_name"`
		Line 		    	  string   	`json:"line"`
		NearText	    	  string   	`json:"near_text"`

	}{
		Time 	    		: d.Time.Format(time.RFC3339),
		Raw 				: d.Raw,
		Number 				: d.Number,
		IsCPF 				: d.IsCPF,
		IsCNPJ 				: d.IsCNPJ,
		Source 				: d.Source,
		FileName 			: d.FileName,
		Line 				: d.Line,
		NearText 			: d.NearText,
	})
}

// LeakIndexable is implemented by every leak type (Credential, URL, Email,
// Phone, Document). It powers the restructured Elasticsearch writer, which
// splits each leak into three concerns:
//
//   - LeakID:   a content-only fingerprint used as the _id in the global,
//               deduplicated leak index. It must NOT depend on the file it
//               was found in nor on a timestamp, so the same leak seen in
//               different files/imports collapses to a single document.
//   - LeakDoc:  the intrinsic leak fields (the value itself). This is all
//               that gets stored in the leak index — no file reference.
//   - RefDoc:   the occurrence-specific context (near_text, line, source...)
//               that belongs to the monthly file<->leak reference index, not
//               to the leak itself.
//   - LeakType: a discriminator string for the reference index.
type LeakIndexable interface {
	LeakID() string
	LeakType() string
	LeakDoc() map[string]interface{}
	RefDoc() map[string]interface{}
}

func (cred Credential) LeakType() string { return "credential" }

func (cred Credential) LeakID() string {
	var hash string
	_calcHash(&hash, cred.Rule, cred.UserDomain, cred.Username, cred.Password, cred.Url)
	return hash
}

func (cred Credential) LeakDoc() map[string]interface{} {
	return map[string]interface{}{
		"rule":        cred.Rule,
		"user_domain": strings.ToLower(cred.UserDomain),
		"username":    cred.Username,
		"password":    cred.Password,
		"cpf":         cred.CPF,
		"url":         cred.Url,
		"url_domain":  strings.ToLower(cred.UrlDomain),
		"severity":    cred.Severity,
		"entropy":     cred.Entropy,
	}
}

func (cred Credential) RefDoc() map[string]interface{} {
	return map[string]interface{}{
		"near_text": cred.NearText,
	}
}

func (u URL) LeakType() string { return "url" }

func (u URL) LeakID() string {
	var hash string
	_calcHash(&hash, u.Url)
	return hash
}

func (u URL) LeakDoc() map[string]interface{} {
	return map[string]interface{}{
		"domain": strings.ToLower(u.Domain),
		"url":    u.Url,
	}
}

func (u URL) RefDoc() map[string]interface{} {
	return map[string]interface{}{
		"near_text": u.NearText,
	}
}

func (eml Email) LeakType() string { return "email" }

func (eml Email) LeakID() string {
	var hash string
	_calcHash(&hash, strings.ToLower(eml.Email))
	return hash
}

func (eml Email) LeakDoc() map[string]interface{} {
	return map[string]interface{}{
		"domain": strings.ToLower(eml.Domain),
		"email":  strings.ToLower(eml.Email),
	}
}

func (eml Email) RefDoc() map[string]interface{} {
	return map[string]interface{}{
		"near_text": eml.NearText,
	}
}

func (p Phone) LeakType() string { return "phone" }

func (p Phone) LeakID() string {
	var hash string
	_calcHash(&hash, p.Phone)
	return hash
}

func (p Phone) LeakDoc() map[string]interface{} {
	return map[string]interface{}{
		"country": p.Country,
		"raw":     p.Raw,
		"phone":   p.Phone,
	}
}

func (p Phone) RefDoc() map[string]interface{} {
	return map[string]interface{}{
		"source":    p.Source,
		"file_name": p.FileName,
		"line":      p.Line,
		"near_text": p.NearText,
	}
}

func (d Document) LeakType() string { return "document" }

func (d Document) LeakID() string {
	var hash string
	_calcHash(&hash, d.Number)
	return hash
}

func (d Document) LeakDoc() map[string]interface{} {
	return map[string]interface{}{
		"raw":     d.Raw,
		"number":  d.Number,
		"is_cpf":  d.IsCPF,
		"is_cnpj": d.IsCNPJ,
	}
}

func (d Document) RefDoc() map[string]interface{} {
	return map[string]interface{}{
		"source":    d.Source,
		"file_name": d.FileName,
		"line":      d.Line,
		"near_text": d.NearText,
	}
}

// CalcRefHash exposes the shared SHA1 hashing used for deterministic document
// ids to other packages (the Elasticsearch writer uses it to key the
// file<->leak reference documents by file_id + leak_id).
func CalcRefHash(outValue *string, keyvals ...interface{}) {
	_calcHash(outValue, keyvals...)
}

func _calcHash(outValue *string, keyvals ...interface{}) {

	data := ""
	for _, v := range keyvals {
		if _, ok := v.(int); ok {
            data += fmt.Sprintf("%d,", v)
        }else if dt, ok := v.(time.Time); ok {
            data += dt.Format(time.RFC3339)
        }else{
            data += fmt.Sprintf("%s,", v)
        }
	}

	h := sha1.New()
	h.Write([]byte(data))

	*outValue = hex.EncodeToString(h.Sum(nil))

}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (file *File) Sanitize() {
	file.Provider = tools.SanitizeUTF8(file.Provider)
	file.FilePath = tools.SanitizeUTF8(file.FilePath)
	file.FileName = tools.SanitizeUTF8(file.FileName)
	file.Name = tools.SanitizeUTF8(file.Name)
	file.Bucket = tools.SanitizeUTF8(file.Bucket)
	file.MediaType = tools.SanitizeUTF8(file.MediaType)
	file.ProviderId = tools.SanitizeUTF8(file.ProviderId)
	file.MIMEType = tools.SanitizeUTF8(file.MIMEType)
	file.Fingerprint = tools.SanitizeUTF8(file.Fingerprint)
	file.Content = tools.SanitizeUTF8(file.Content)
	file.FailedReason = tools.SanitizeUTF8(file.FailedReason)

	for i := range file.Credentials {
		file.Credentials[i].Sanitize()
	}
	for i := range file.Emails {
		file.Emails[i].Sanitize()
	}
	for i := range file.URLs {
		file.URLs[i].Sanitize()
	}
	for i := range file.Phones {
		file.Phones[i].Sanitize()
	}
	for i := range file.Documents {
		file.Documents[i].Sanitize()
	}
}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (cred *Credential) Sanitize() {
	cred.Rule = tools.SanitizeUTF8(cred.Rule)
	cred.UserDomain = tools.SanitizeUTF8(cred.UserDomain)
	cred.Username = tools.SanitizeUTF8(cred.Username)
	cred.Password = tools.SanitizeUTF8(cred.Password)
	cred.CPF = tools.SanitizeUTF8(cred.CPF)
	cred.Url = tools.SanitizeUTF8(cred.Url)
	cred.UrlDomain = tools.SanitizeUTF8(cred.UrlDomain)
	cred.NearText = tools.SanitizeUTF8(cred.NearText)
}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (eml *Email) Sanitize() {
	eml.Domain = tools.SanitizeUTF8(eml.Domain)
	eml.Email = tools.SanitizeUTF8(eml.Email)
	eml.NearText = tools.SanitizeUTF8(eml.NearText)
}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (u *URL) Sanitize() {
	u.Domain = tools.SanitizeUTF8(u.Domain)
	u.Url = tools.SanitizeUTF8(u.Url)
	u.NearText = tools.SanitizeUTF8(u.NearText)
}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (p *Phone) Sanitize() {
	p.Country = tools.SanitizeUTF8(p.Country)
	p.Raw = tools.SanitizeUTF8(p.Raw)
	p.Phone = tools.SanitizeUTF8(p.Phone)
	p.Source = tools.SanitizeUTF8(p.Source)
	p.FileName = tools.SanitizeUTF8(p.FileName)
	p.Line = tools.SanitizeUTF8(p.Line)
	p.NearText = tools.SanitizeUTF8(p.NearText)
}

// Sanitize removes null bytes and invalid UTF-8 sequences from all string fields
func (d *Document) Sanitize() {
	d.Raw = tools.SanitizeUTF8(d.Raw)
	d.Number = tools.SanitizeUTF8(d.Number)
	d.Source = tools.SanitizeUTF8(d.Source)
	d.FileName = tools.SanitizeUTF8(d.FileName)
	d.Line = tools.SanitizeUTF8(d.Line)
	d.NearText = tools.SanitizeUTF8(d.NearText)
}