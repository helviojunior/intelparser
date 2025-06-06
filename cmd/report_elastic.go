package cmd

import (
    "errors"
    "path/filepath"
    "strings"
    "sync"
    "time"
    "os"

    "golang.org/x/term"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/tools"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/writers"
    resolver "github.com/helviojunior/gopathresolver"
    "github.com/spf13/cobra"
    
)

var elkCmdExtensions = []string{".sqlite3", ".db", ".jsonl"}
var elkCmdFlags = struct {
    fromFile string
    fromExt string
    elasticURI string
}{}
var elkCmd = &cobra.Command{
    Use:   "elastic",
    Short: "Sync from local SQLite or JSON Lines file formats to Elastic",
    Long: ascii.LogoHelp(ascii.Markdown(`
# report elastic

Sync from local SQLite or JSON Lines file formats to Elastic.

A --from-file and --elasticsearch-uri must be specified.`)),
    Example: ascii.Markdown(`
   - intelparser report elastic --elasticsearch-uri http://localhost:9200/intelparser
   - intelparser report elastic --elasticsearch-uri http://localhost:9200/intelparser --filter sec4us,webapi,hookchain
   - intelparser report elastic --from-file intelparser.sqlite3 --elasticsearch-uri http://localhost:9200/intelparser
   - intelparser report elastic --from-file intelparser.jsonl --elasticsearch-uri http://localhost:9200/intelparser`),
    PreRunE: func(cmd *cobra.Command, args []string) error {
        var err error
        if elkCmdFlags.fromFile == "" {
            return errors.New("from file not set")
        }

        elkCmdFlags.fromFile, err = resolver.ResolveFullPath(elkCmdFlags.fromFile)
        if err != nil {
            return err
        }

        elkCmdFlags.fromExt = strings.ToLower(filepath.Ext(elkCmdFlags.fromFile))

        if elkCmdFlags.fromExt == "" {
            return errors.New("source file must have extension")
        }

        if !tools.SliceHasStr(elkCmdExtensions, elkCmdFlags.fromExt) {
            return errors.New("unsupported from file type")
        }

        if !tools.FileExists(elkCmdFlags.fromFile) {
            return errors.New("Source file not found") 
        }

        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {
        var writer writers.Writer
        var err error
        var running bool
        wg := sync.WaitGroup{}

        log.Info("Checking Elasticsearch indexes...")
        writer, err = writers.NewElasticWriter(elkCmdFlags.elasticURI)
        if err != nil {
            log.Error("could not get a elastic writer up", "err", err)
            return
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
            if elkCmdFlags.fromExt == ".sqlite3" || elkCmdFlags.fromExt == ".db" {
                if err := convertFromDbTo(elkCmdFlags.fromFile, writer, status); err != nil {
                    log.Error("failed to convert from SQLite", "err", err)
                    return
                }
            } else if elkCmdFlags.fromExt == ".jsonl" {
                if err := convertFromJsonlTo(elkCmdFlags.fromFile, writer, status); err != nil {
                    log.Error("failed to convert from JSON Lines", "err", err)
                    return
                } 
            }
            running = false
            time.Sleep(time.Duration(time.Second/4))
        }()

        wg.Wait()
        
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

    },
}

func init() {
    reportCmd.AddCommand(elkCmd)

    elkCmd.Flags().StringVar(&elkCmdFlags.fromFile, "from-file", "~/.intelparser.db", "The file to convert from. Use .sqlite3 for conversion from SQLite, and .jsonl for conversion from JSON Lines")
    elkCmd.Flags().StringVar(&elkCmdFlags.elasticURI, "elasticsearch-uri", "http://localhost:9200/intelparser", "The elastic search URI to use. (e.g., http://user:pass@host:9200/index)")

}
