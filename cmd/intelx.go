package cmd

import (
    "errors"
    "log/slog"
    "path/filepath"
    //"fmt"
    "os"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/islazy"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/runner"
    //"github.com/helviojunior/intelparser/pkg/database"
    //"github.com/helviojunior/intelparser/pkg/writers"
    "github.com/helviojunior/intelparser/pkg/readers"
    parsers "github.com/helviojunior/intelparser/pkg/runner/parsers"
    //"gorm.io/gorm"
    "github.com/spf13/cobra"
)

func AddZipFile(temp_folder string, file_path string) error {
    var mime string
    var dst string
    var err error

    if mime, err = islazy.GetMimeType(file_path); err != nil {
        return err
    }

    if mime != "application/zip" {
        return errors.New("invalid file type")
    }

    dst, err = islazy.CreateDirFromFilename(temp_folder, file_path)
    if err = islazy.Unzip(file_path, dst); err != nil {
        return err
    }

    err = AddFolder(temp_folder, dst);

    return err

}

func AddFolder(temp_folder string, folder_path string) error {
    //scanRunner.Files <- intelxCmdOptions.Path

    entries, err := os.ReadDir(folder_path)
    if err != nil {
        return err
    }
 
    info := filepath.Join(folder_path, "Info.csv")
    if !islazy.FileExists(info) {
        return errors.New("File 'Info.csv' not found") 
    }

    if err := scanRunner.ParsePositionalFile(info); err != nil {
        return err
    }

    for _, e := range entries {
        if e.Name() != "Info.csv" {
            scanRunner.Files <- filepath.Join(folder_path, e.Name())
        }
    }

    return nil
}

var parserDriver runner.ParserDriver

var intelxCmdOptions = &readers.FileReaderOptions{}
var intelxCmd = &cobra.Command{
    Use:   "intelx",
    Short: "Parse IntelX downloaded files",
    Long: ascii.LogoHelp(ascii.Markdown(`
# parse intelx

Parse IntelX downloaded files (ZIP or folder).

`)),
    Example: `
   - intelparser parse intelx -p "~/Desktop/Search 2025-02-05 10_48_28.zip"
   - intelparser parse intelx -p "~/Desktop/"
   - intelparser parse intelx -p ~/Desktop/ --write-elastic --write-elasticsearch-uri "http://127.0.0.1:9200/intelparser"
`,
    PreRunE: func(cmd *cobra.Command, args []string) error {
        var err error

        if intelxCmdOptions.Path == "" && len(intelxCmdOptions.Path) == 0 {
            return errors.New("a ZIP file or path must be specified")
        }

        if intelxCmdOptions.Path != "" && !islazy.FileExists(intelxCmdOptions.Path) {
            return errors.New("ZIP file or path is not readable")
        }

        // An slog-capable logger to use with drivers and runners
        logger := slog.New(log.Logger)

        // Configure the driver
        parserDriver, err = parsers.NewInteX(logger, *opts)
        if err != nil {
            return err
        }

        // Get the runner up. Basically, all of the subcommands will use this.
        scanRunner, err = runner.NewRunner(logger, parserDriver, *opts, scanWriters)
        if err != nil {
            return err
        }

        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {
        var ft string
        var err error

        if ft, err = islazy.FileType(intelxCmdOptions.Path); err != nil {
            log.Error("error getting path type", "err", err)
            os.Exit(2)
        }

        log.Debug("starting parsing scanning", "path", intelxCmdOptions.Path, "type", ft)

        go func() {
            defer close(scanRunner.Files)

            if ft == "file" {
                //File
                if err = AddZipFile(tempFolder, intelxCmdOptions.Path); err != nil {
                    log.Error("error parsing ZIP file", "err", err)
                }

            }else{
                //Directory

                info := filepath.Join(intelxCmdOptions.Path, "Info.csv")
                if islazy.FileExists(info) {
                    
                    if err = AddFolder(tempFolder, intelxCmdOptions.Path); err != nil {
                        log.Error("error", "err", err)
                    }

                }else{

                    entries, err := os.ReadDir(intelxCmdOptions.Path)
                    if err != nil {
                        log.Error("error listting directory files", "err", err)
                        os.Exit(2)
                    }

                    for _, e := range entries {
                        AddZipFile(tempFolder, filepath.Join(intelxCmdOptions.Path, e.Name()))
                    }
                }

            }

        }()

        status := scanRunner.Run()
        scanRunner.Close()

        st := "Execution statistics\n"
        st += "     -> Parsed...........: %d\n"
        st += "     -> Skipped..........: %d\n"
        st += "     -> Execution error..: %d\n"
        st += "     -> Credentials......: %d\n"
        st += "     -> URLs.............: %d\n"
        st += "     -> E-mails..........: %d\n"

        log.Warnf(st, 
             status.Parsed, 
             status.Skipped,
             status.Error,
             status.Credential,
             status.Url,
             status.Email,
        )

        islazy.RemoveFolder(tempFolder)

    },
}

func init() {
    parserCmd.AddCommand(intelxCmd)

    intelxCmd.Flags().StringVarP(&intelxCmdOptions.Path, "path", "p", "", "A Path with IntelX file(s).")
}