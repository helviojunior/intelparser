package writers

import "github.com/helviojunior/intelparser/pkg/models"

// Writer is a results writer
type Writer interface {
	Write(*models.File) error
}
