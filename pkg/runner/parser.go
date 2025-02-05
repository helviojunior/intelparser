package runner

import (
	"fmt"
	//"bufio"
	//"bytes"
	//"io"

	"github.com/helviojunior/intelparser/pkg/models"
)

// ChromeNotFoundError signals that chrome is not available
type ParserNotFoundError struct {
	Err error
}

func (e ParserNotFoundError) Error() string {
	return fmt.Sprintf("parser not found: %v", e.Err)
}

// Parser is the interface file drivers will implement.
type ParserDriver interface {
	ParseFile(runner *Runner, file_path string) (*models.File, error)
	Close()
}

