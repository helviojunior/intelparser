package runner

import (
	"fmt"
	//"bufio"
	//"bytes"
	//"io"

	"github.com/helviojunior/intelparser/pkg/models"
)

type FileItem struct {
	RealPath     string
	VirtualPath  string
}

// ChromeNotFoundError signals that chrome is not available
type ParserNotFoundError struct {
	Err error
}

func (e ParserNotFoundError) Error() string {
	return fmt.Sprintf("parser not found: %v", e.Err)
}

// Parser is the interface file drivers will implement.
type ParserDriver interface {
	ParseFile(runner *Runner, file FileItem) (*models.File, error)
	Close()
}

