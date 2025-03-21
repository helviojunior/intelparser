package readers

import (
	"os"
	"bufio"
	"strings"
)

// FileReaderOptions are options for the file reader
type FileReaderOptions struct {
    Path    string
}

// Read from a file.
func ReadFileList(fileName string, outList *[]string) error {

	var file *os.File
	var err error

	file, err = os.Open(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		candidate := scanner.Text()
		if candidate == "" {
			continue
		}

		*outList = append(*outList, strings.ToLower(candidate))
	}

	return scanner.Err()
}