package writers

import (
	"github.com/helviojunior/intelparser/pkg/models"
)

// NoneWriter is a None writer
type NoneWriter struct {
}

// NewNoneWriter initialises a none writer
func NewNoneWriter() (*NoneWriter, error) {
	return &NoneWriter{}, nil
}

// Write does nothing
func (s *NoneWriter) Write(result *models.File) error {
	return nil
}
