package writers

import (
	//"fmt"
	//"os"

	"github.com/helviojunior/intelparser/pkg/models"
	logger "github.com/helviojunior/intelparser/pkg/log"
)

// StdoutWriter is a Stdout writer
type StdoutWriter struct {
}

// NewStdoutWriter initialises a stdout writer
func NewStdoutWriter() (*StdoutWriter, error) {
	return &StdoutWriter{}, nil
}

// Write results to stdout
func (s *StdoutWriter) Write(result *models.File) error {
	logger.Debugf("Finishing %s", result.FileName)
	return nil
}
