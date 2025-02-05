package writers

import (
	"sync"

	"github.com/helviojunior/intelparser/internal/islazy"
	"github.com/helviojunior/intelparser/pkg/database"
	//"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/models"
	"gorm.io/gorm"
)

var hammingThreshold = 10

// DbWriter is a Database writer
type DbWriter struct {
	URI           string
	conn          *gorm.DB
	mutex         sync.Mutex
	hammingGroups []islazy.HammingGroup
}

// NewDbWriter initialises a database writer
func NewDbWriter(uri string, debug bool) (*DbWriter, error) {
	c, err := database.Connection(uri, false, debug)
	if err != nil {
		return nil, err
	}

	return &DbWriter{
		URI:           uri,
		conn:          c,
		mutex:         sync.Mutex{},
		hammingGroups: []islazy.HammingGroup{},
	}, nil
}

// Write results to the database
func (dw *DbWriter) Write(result *models.File) error {
	dw.mutex.Lock()
	defer dw.mutex.Unlock()
	return dw.conn.CreateInBatches(result, 50).Error
}
