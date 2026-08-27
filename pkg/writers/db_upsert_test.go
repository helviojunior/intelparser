package writers

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/helviojunior/intelparser/pkg/models"
	"gorm.io/gorm"
)

func mkFile(name, fingerprint, user string) *models.File {
	return &models.File{
		Provider:    "IntelX",
		FileName:    name,
		FilePath:    "zip/" + name,
		Fingerprint: fingerprint,
		IndexedAt:   time.Now(),
		Credentials: []models.Credential{{Rule: "r", Username: user, Password: "p"}},
	}
}

// TestUpsertOnFingerprint covers the uni_files_fingerprint violation: the same
// content arriving under a second file name must update the existing row rather
// than fail with SQLSTATE 23505, and the cascaded leak inserts must still work.
func TestUpsertOnFingerprint(t *testing.T) {
	w, err := NewDbWriter("sqlite:///"+filepath.Join(t.TempDir(), "t.sqlite3"), false)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	// Each query gets a fresh session: the writer's *gorm.DB carries an
	// OnConflict clause and would otherwise leak conditions between calls.
	q := func() *gorm.DB { return w.conn.Session(&gorm.Session{NewDB: true}) }

	if err := w.Write(mkFile("first.csv", "fp-a", "alice")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := w.Write(mkFile("second.csv", "fp-a", "alice")); err != nil {
		t.Fatalf("second write (the reported 23505): %v", err)
	}

	var files []models.File
	if err := q().Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files rows = %d, want 1", len(files))
	}
	if files[0].FileName != "second.csv" {
		t.Errorf("file_name = %q, want second.csv (row must be updated)", files[0].FileName)
	}

	var creds []models.Credential
	if err := q().Find(&creds).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if c.FileID != files[0].ID {
			t.Errorf("credential %d points at file_id %d, want %d", c.ID, c.FileID, files[0].ID)
		}
	}
	t.Logf("identical re-import: files=%d credentials=%d", len(files), len(creds))
}

// A distinct fingerprint must still create its own row.
func TestDistinctFingerprintInsertsNewRow(t *testing.T) {
	w, err := NewDbWriter("sqlite:///"+filepath.Join(t.TempDir(), "t.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(mkFile("a.csv", "fp-a", "alice")); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(mkFile("b.csv", "fp-b", "bob")); err != nil {
		t.Fatal(err)
	}
	var files []models.File
	if err := w.conn.Session(&gorm.Session{NewDB: true}).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("files rows = %d, want 2", len(files))
	}
}
