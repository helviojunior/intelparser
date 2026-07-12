package models

import (
	"testing"
	"time"
)

// The leak _id must depend ONLY on the leak content — not on the file it was
// found in nor on when it was seen — so the same leak collapses to one document
// across files and imports (global dedup).
func TestLeakIDIsContentOnly(t *testing.T) {
	base := Credential{
		Rule:       "generic",
		UserDomain: "example.com",
		Username:   "alice",
		Password:   "s3cr3t",
		Url:        "https://example.com/login",
		Time:       time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NearText:   "context A",
		Severity:   5,
	}

	// Same content, different Time / NearText / Severity => same LeakID.
	other := base
	other.Time = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	other.NearText = "totally different context B"
	other.Severity = 1

	if base.LeakID() != other.LeakID() {
		t.Fatalf("LeakID changed with non-content fields: %q != %q", base.LeakID(), other.LeakID())
	}

	// Different password => different LeakID.
	diff := base
	diff.Password = "other"
	if base.LeakID() == diff.LeakID() {
		t.Fatalf("LeakID collided for different content")
	}
}

// The leak document must carry only the intrinsic value, never occurrence
// context (near_text) nor any file reference / timestamp.
func TestLeakDocHasNoOccurrenceOrReference(t *testing.T) {
	c := Credential{Username: "u", Password: "p", NearText: "ctx"}
	doc := c.LeakDoc()

	for _, forbidden := range []string{"near_text", "file_id", "fingerprint", "time", "bucket", "inserted_at", "last_reference_at"} {
		if _, ok := doc[forbidden]; ok {
			t.Errorf("LeakDoc must not contain %q", forbidden)
		}
	}
	if doc["username"] != "u" || doc["password"] != "p" {
		t.Errorf("LeakDoc missing intrinsic fields: %#v", doc)
	}

	// Occurrence context belongs to the reference document.
	ref := c.RefDoc()
	if ref["near_text"] != "ctx" {
		t.Errorf("RefDoc must carry near_text, got %#v", ref)
	}
}

// Phone/Document occurrence fields (source/line/file_name) live on the ref doc,
// not on the leak.
func TestPhoneSplit(t *testing.T) {
	p := Phone{Country: "BR", Phone: "+5511999998888", Raw: "(11) 99999-8888",
		Source: "file.txt", Line: "call 11999998888", FileName: "leak.zip", NearText: "ctx"}

	leak := p.LeakDoc()
	if leak["phone"] != "+5511999998888" {
		t.Errorf("leak missing phone: %#v", leak)
	}
	for _, forbidden := range []string{"source", "line", "file_name", "near_text"} {
		if _, ok := leak[forbidden]; ok {
			t.Errorf("phone LeakDoc must not contain %q", forbidden)
		}
	}
	ref := p.RefDoc()
	for _, want := range []string{"source", "line", "file_name", "near_text"} {
		if _, ok := ref[want]; !ok {
			t.Errorf("phone RefDoc missing %q: %#v", want, ref)
		}
	}
}

// CalcRefHash is deterministic and order-sensitive on (file_id, leak_id).
func TestCalcRefHash(t *testing.T) {
	var a, b, c string
	CalcRefHash(&a, "file1", "leak1")
	CalcRefHash(&b, "file1", "leak1")
	CalcRefHash(&c, "leak1", "file1")
	if a == "" || a != b {
		t.Fatalf("CalcRefHash not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("CalcRefHash should depend on argument order")
	}
}
