//go:build !windows
// +build !windows

package log

import (
    "fmt"
    "os"
)

// ClearLine clears the current line and moves the cursor to it's start position.
func clearLine() {
    fmt.Fprintf(os.Stderr, "\x1b[2K")
}
