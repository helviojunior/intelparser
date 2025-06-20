package cmd

import (

    "errors"
    "fmt"
    "sync"
    "time"
    "path/filepath"
    "strings"
    "os"

    "golang.org/x/term"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/tools"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/writers"
    resolver "github.com/helviojunior/gopathresolver"
    "github.com/spf13/cobra"
)

var conversionCmdExtensions = []string{".sqlite3", ".db", ".jsonl"}
var convertCmdFlags = struct {
    fromFile string
    toFile   string

    fromExt string
    toExt   string
}{}
var convertCmd = &cobra.Command{
    Use:   "convert",
    Short: "Convert between SQLite and JSON Lines file formats",
    Long: ascii.LogoHelp(ascii.Markdown(`
# report convert

Convert between SQLite and JSON Lines file formats.

A --from-file and --to-file must be specified. The extension used for the
specified filenames will be used to determine the conversion direction and
target.`)),
    Example: `
   - intelparser report convert --to-file data.jsonl
   - intelparser report convert --to-file data.jsonl --filter sec4us,webapi,hookchain
   - intelparser report convert --from-file intelparser.sqlite3 --to-file data.jsonl
   - intelparser report convert --from-file intelparser.jsonl --to-file db.sqlite3`,
    PreRunE: func(cmd *cobra.Command, args []string) error {
        var err error

        if convertCmdFlags.fromFile == "" {
            return errors.New("from file not set")
        }
        if convertCmdFlags.toFile == "" {
            return errors.New("to file not set")
        }

        convertCmdFlags.fromFile, err = resolver.ResolveFullPath(convertCmdFlags.fromFile)
        if err != nil {
            return err
        }

        convertCmdFlags.toFile, err = resolver.ResolveFullPath(convertCmdFlags.toFile)
        if err != nil {
            return err
        }

        convertCmdFlags.fromExt = strings.ToLower(filepath.Ext(convertCmdFlags.fromFile))
        convertCmdFlags.toExt = strings.ToLower(filepath.Ext(convertCmdFlags.toFile))

        if convertCmdFlags.fromExt == "" || convertCmdFlags.toExt == "" {
            return errors.New("source and destination files must have extensions")
        }

        if convertCmdFlags.fromFile == convertCmdFlags.toFile {
            return errors.New("source and destination files cannot be the same")
        }

        if !tools.SliceHasStr(conversionCmdExtensions, convertCmdFlags.fromExt) {
            return errors.New("unsupported from file type")
        }
        if !tools.SliceHasStr(conversionCmdExtensions, convertCmdFlags.toExt) {
            return errors.New("unsupported to file type")
        }

        if !tools.FileExists(convertCmdFlags.fromFile) {
            return errors.New("Source file not found") 
        }

        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {
        var writer writers.Writer
        var err error
        var running bool
        wg := sync.WaitGroup{}

        if convertCmdFlags.toExt == ".sqlite3" || convertCmdFlags.toExt == ".db" {
            writer, err = writers.NewDbWriter(fmt.Sprintf("sqlite:///%s", convertCmdFlags.toFile), false)
            if err != nil {
                log.Error("could not get a database writer up", "err", err)
                return
            }
        } else if convertCmdFlags.toExt == ".jsonl" {
            toFile, err := tools.CreateFileWithDir(convertCmdFlags.toFile)
            if err != nil {
                log.Error("could not create target file", "err", err)
                return
            }
            writer, err = writers.NewJsonWriter(toFile)
            if err != nil {
                log.Error("could not get a JSON writer up", "err", err)
                return
            }
        }

        var status = &ConvStatus{
            Converted: 0,
            Url: 0,
            Email: 0,
            Credential: 0,
            Spin: "",
            IsTerminal: term.IsTerminal(int(os.Stdin.Fd())),
        }

        running = true
        wg.Add(1)
        go func() {
            defer wg.Done()
            for running {
                status.Print()
                if status.IsTerminal {
                    time.Sleep(time.Duration(time.Second / 4))
                }else{
                    time.Sleep(time.Duration(time.Second * 30))
                }
            }
        }()

        wg.Add(1)
        go func() {
            defer wg.Done()
            if convertCmdFlags.fromExt == ".sqlite3" || convertCmdFlags.fromExt == ".db" {
                if err := convertFromDbTo(convertCmdFlags.fromFile, writer, status); err != nil {
                    log.Error("failed to convert to JSON Lines", "err", err)
                    return
                }
            } else if convertCmdFlags.fromExt == ".jsonl" {
                if err := convertFromJsonlTo(convertCmdFlags.fromFile, writer, status); err != nil {
                    log.Error("failed to convert to SQLite", "err", err)
                    return
                }
            }

            running = false
            time.Sleep(time.Second)
        }()

        wg.Wait()
        
        fmt.Fprintf(os.Stderr, "%s\n%s\r\033[A", 
            "                                                                                ",
            "                                                                                ",
        )

        diff := time.Now().Sub(startTime)
        out := time.Time{}.Add(diff)

        st := "Convertion status\n"
        st += "     -> Elapsed time.....: %s\n"
        st += "     -> Files converted..: %s\n"
        st += "     -> Credentials......: %s\n"
        st += "     -> URLs.............: %s\n"
        st += "     -> E-mails..........: %s\n"

        log.Infof(st, 
            out.Format("15:04:05"),
            tools.FormatIntComma(status.Converted), 
            tools.FormatIntComma(status.Credential),
            tools.FormatIntComma(status.Url),
            tools.FormatIntComma(status.Email),
        )

        if (status.Credential + status.Url + status.Email) == 0 {
            log.Warn("No records were converted. Cleaning up output file...")

            err = os.Remove(convertCmdFlags.toFile)
            if err != nil {
                log.Debug("failed deleting file", "file", convertCmdFlags.toFile, "err", err)
            }
        }

    },
}

func init() {
    reportCmd.AddCommand(convertCmd)

    convertCmd.Flags().StringVar(&convertCmdFlags.fromFile, "from-file", "~/.intelparser.db", "The file to convert from")
    convertCmd.Flags().StringVar(&convertCmdFlags.toFile, "to-file", "", "The file to convert to. Use .sqlite3 for conversion to SQLite, and .jsonl for conversion to JSON Lines")
}
