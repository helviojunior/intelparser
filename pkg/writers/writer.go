package writers

import "github.com/helviojunior/intelparser/pkg/models"

// Writer is a results writer
type Writer interface {
	Write(*models.File) error
}

// FlushableWriter is optionally implemented by writers that buffer work
// asynchronously. Callers should invoke Flush after all producers have
// stopped, to drain pending writes before shutdown.
type FlushableWriter interface {
	Flush() error
}

// FinalizableWriter is optionally implemented by writers that can report a
// final summary after all writes are flushed. The runner calls Finalize once
// per writer at the end of a run, after Flush. Used (for example) by the
// Elastic writer to print a table of server-side statistics.
type FinalizableWriter interface {
	Finalize() error
}
