package writers

import (
	"sync"

	//"github.com/helviojunior/intelparser/internal/tools"
	"github.com/helviojunior/intelparser/pkg/database"
	//"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DbWriter is a Database writer
type DbWriter struct {
	URI           string
	ControlOnly   bool
	conn          *gorm.DB
	mutex         sync.Mutex
	ReadOnly      bool
}

// NewDbWriter initialises a database writer
func NewDbWriter(uri string, debug bool) (*DbWriter, error) {
	c, err := database.Connection(uri, false, debug)
	if err != nil {
		return nil, err
	}

	if _, ok := c.Statement.Clauses["ON CONFLICT"]; !ok {
		c = c.Clauses(clause.OnConflict{UpdateAll: true})
	}

	return &DbWriter{
		URI:           uri,
		ControlOnly:   false,
		conn:          c,
		mutex:         sync.Mutex{},
		ReadOnly:      false,
	}, nil
}

// Write results to the database
func (dw *DbWriter) Write(result *models.File) error {

	if dw.ReadOnly {
		return nil
	}

	dw.mutex.Lock()
	defer dw.mutex.Unlock()

	if dw.ControlOnly {
		//Save onl
		r1 := result.Clone()
		r1.Content = ""
		return dw.conn.Session(&gorm.Session{CreateBatchSize: 200}).Create(r1).Error
	}

	return dw.conn.Session(&gorm.Session{CreateBatchSize: 200}).Create(result).Error
}
