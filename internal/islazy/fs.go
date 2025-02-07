package islazy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"net/http"
	"errors"
	"crypto/sha1"
    "encoding/hex"
    "bufio"
    "bytes"

	"archive/zip"
    
)

func GetMimeType(s string) (string, error) {
	file, err := os.Open(s)

     if err != nil {
         return "", err
     }

     defer file.Close()

     buff := make([]byte, 512)

     // why 512 bytes ? see http://golang.org/pkg/net/http/#DetectContentType
     _, err = file.Read(buff)

     if err != nil {
        return "", err
     }

     filetype := http.DetectContentType(buff)
     if strings.Contains(filetype, ";") {
     	s1 := strings.SplitN(filetype, ";", 2)
     	if s1[0] != "" && strings.Contains(s1[0], "/") {
     		filetype = s1[0]
     	}
     } 

     return filetype, nil
}

// CreateDir creates a directory if it does not exist, returning the final
// normalized directory as a result.
func CreateDir(dir string) (string, error) {
	var err error

	if strings.HasPrefix(dir, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(homeDir, dir[1:])
	}

	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	return dir, nil
}

// CreateFileWithDir creates a file, relative to a directory, returning the
// final normalized path as a result.
func CreateFileWithDir(destination string) (string, error) {
	dir := filepath.Dir(destination)
	file := filepath.Base(destination)

	if file == "." || file == "/" {
		return "", fmt.Errorf("destination does not appear to be a valid file path: %s", destination)
	}

	absDir, err := CreateDir(dir)
	if err != nil {
		return "", err
	}

	absPath := filepath.Join(absDir, file)
	fileHandle, err := os.Create(absPath)
	if err != nil {
		return "", err
	}
	defer fileHandle.Close()

	return absPath, nil
}

func CreateDirFromFilename(destination string, s string) (string, error) {
	fn := SafeFileName(strings.TrimSuffix(filepath.Base(s), filepath.Ext(s)))
	if fn == "" {
		fn = "temp"
	}

	return CreateDir(filepath.Join(destination, fn))
}

// SafeFileName takes a string and returns a string safe to use as
// a file name.
func SafeFileName(s string) string {
	var builder strings.Builder

	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('-')
		}
	}

	return builder.String()
}

// FileExists returns true if a path exists
func FileExists(path string) bool {
	_, err := os.Stat(path)

	return !os.IsNotExist(err)
}

func FileType(path string) (string, error) {
    fi, err := os.Stat(path)

    if err != nil {
    	return "", err
    }

	switch mode := fi.Mode(); {
	    case mode.IsDir():
	        // do directory stuff
	        return "directory", nil
	    case mode.IsRegular():
	        // do file stuff
	        return "file", nil
	    }

	return "undefined", nil
}

// MoveFile moves a file from a to b
func MoveFile(sourcePath, destPath string) error {
	if err := os.Rename(sourcePath, destPath); err == nil {
		return nil
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	err = os.Remove(sourcePath)
	if err != nil {
		return err
	}

	return nil
}

func RemoveFolder(path string) error {

	fi, err := os.Stat(path)

    if err != nil {
    	return err
    }

    if fi.Mode().IsDir() {
    	err = os.RemoveAll(path)
		if err != nil {
			return err
		}

    }else{
    	return errors.New("Path is not a Directory!") 
    }

    return nil
}

func Unzip(src, dest string) error {
    r, err := zip.OpenReader(src)
    if err != nil {
        return err
    }
    defer r.Close()

    for _, f := range r.File {
        rc, err := f.Open()
        if err != nil {
            return err
        }
        defer rc.Close()

        fpath := filepath.Join(dest, f.Name)
        if f.FileInfo().IsDir() {
            os.MkdirAll(fpath, f.Mode())
        } else {
            var fdir string
            if lastIndex := strings.LastIndex(fpath,string(os.PathSeparator)); lastIndex > -1 {
                fdir = fpath[:lastIndex]
            }

            err = os.MkdirAll(fdir, f.Mode())
            if err != nil {
                return err
            }
            f, err := os.OpenFile(
                fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
            if err != nil {
                return err
            }
            defer f.Close()

            _, err = io.Copy(f, rc)
            if err != nil {
                return err
            }
        }
    }
    return nil
}

func GetHash(data []byte) string {
	h := sha1.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func GetHashFromFile(file_path string) (string, error) {
	f, err := os.Open(file_path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil

}

func ReadTextFile(file_path string) (string, error) {
	f, err := os.Open(file_path)
    if err != nil {
        return "", err
    }
    defer f.Close()

    br := bufio.NewReader(f)
    r, _, err := br.ReadRune()
    if err != nil {
        return "", err
    }
    if r != '\uFEFF' {
        br.UnreadRune() // Not a BOM -- put the rune back
    }

	buf := new(bytes.Buffer)
	buf.ReadFrom(br)

    return buf.String(), nil
}
