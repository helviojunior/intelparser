package cmd

import (
    "errors"
    "log/slog"
    "path/filepath"
    //"fmt"
    "os"
    "time"
    "strings"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/tools"
    "github.com/helviojunior/intelparser/internal/disk"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/runner"
    //"github.com/helviojunior/intelparser/pkg/database"
    //"github.com/helviojunior/intelparser/pkg/writers"
    "github.com/helviojunior/intelparser/pkg/readers"
    parsers "github.com/helviojunior/intelparser/pkg/runner/parsers"
    resolver "github.com/helviojunior/gopathresolver"
    //"gorm.io/gorm"
    "github.com/spf13/cobra"
)

func AddZipFile(temp_folder string, file_path string) error {
    var mime string
    var dst string
    var err error
    file_name := filepath.Base(file_path)
    logger := log.With("file", file_name)

    logger.Debug("Checking file")
    if mime, err = tools.GetMimeType(file_path); err != nil {
        logger.Debug("Error getting mime type", "err", err)
        return err
    }

    logger.Debug("Mime type", "mime", mime)
    if mime != "application/zip" {
        return errors.New("invalid file type")
    }

    if dst, err = tools.CreateDirFromFilename(temp_folder, file_path); err != nil {
        logger.Debug("Error creating temp folder to extract zip file", "err", err)
        return err
    }

    if err = tools.Unzip(file_path, dst); err != nil {
        logger.Debug("Error extracting zip file", "temp_folder", dst, "err", err)
        return err
    }

    return AddFolder(temp_folder, dst, file_path);

}

func AddFolder(temp_folder string, folder_path string, zip_source string) error {
    //scanRunner.Files <- intelxCmdOptions.Path

    entries, err := os.ReadDir(folder_path)
    if err != nil {
        return err
    }
 
    log.Debug("Checking folder", "path", folder_path)
    info := filepath.Join(folder_path, "Info.csv")
    if !tools.FileExists(info) {
        return errors.New("File 'Info.csv' not found") 
    }

    if zip_source == "" {
        log.Info("Parsing files in folder", "folder", folder_path)
    }else {
        file_name := filepath.Base(zip_source)
        log.Info("Parsing ZIP file", "file", file_name)
    }

    if err := scanRunner.ParsePositionalFile(info); err != nil {
        return err
    }

    for _, e := range entries {
        if e.Name() != "Info.csv" && e.Name() != "info.sqlite3" {
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

        if intelxCmdOptions.Path != "" && !tools.FileExists(intelxCmdOptions.Path) {
            return errors.New("ZIP file or path is not readable")
        }

        intelxCmdOptions.Path, err = resolver.ResolveFullPath(intelxCmdOptions.Path)
        if err != nil {
            return err
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

        if ft, err = tools.FileType(intelxCmdOptions.Path); err != nil {
            log.Error("error getting path type", "err", err)
            os.Exit(2)
        }

        log.Debug("starting parsing scanning", "path", intelxCmdOptions.Path, "type", ft)

        di, err := disk.GetInfo(tempFolder, false)
        if err != nil {
            log.Error("Error getting disk stats", "path", tempFolder, "err", err)
            os.Exit(2)
        }

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
                if tools.FileExists(info) {
                    
                    if err = AddFolder(tempFolder, intelxCmdOptions.Path, ""); err != nil {
                        log.Error("error", "err", err)
                    }

                }else{

                    entries, err := os.ReadDir(intelxCmdOptions.Path)
                    if err != nil {
                        log.Error("Rrror listting directory files", "err", err)
                        os.Exit(2)
                    }

                    for _, e := range entries {
                        if strings.ToLower(filepath.Ext(e.Name())) != ".zip" {
                            log.Debug("Ignoring non ZIP file", "file", e.Name())
                            continue
                        }

                        fst, err := os.Stat(filepath.Join(intelxCmdOptions.Path, e.Name()))
                        if err != nil {
                            log.Error("Error getting file stats", "file", e.Name(), "err", err)
                            os.Exit(2)
                        }

                        if di.Free <= uint64(5 * fst.Size()) {
                            log.Error("No space left on temp path", "temp_path", tempFolder)
                            os.Exit(2)
                        }

                        err = AddZipFile(tempFolder, filepath.Join(intelxCmdOptions.Path, e.Name()))
                        if err != nil {
                            log.Debug("Error checking ZIP file", "file", e.Name(), "err", err)
                        }
                    }
                }

            }

        }()

        log.Info("Starting InteX parser")
        status := scanRunner.Run()
        scanRunner.Close()

        diff := time.Now().Sub(startTime)
        out := time.Time{}.Add(diff)

        st := "Execution statistics\n"
        st += "     -> Elapsed time.....: %s\n"
        st += "     -> Files parsed.....: %s\n"
        st += "     -> Skipped..........: %s\n"
        st += "     -> Execution error..: %s\n"
        st += "     -> Credentials......: %s\n"
        st += "     -> URLs.............: %s\n"
        st += "     -> E-mails..........: %s\n"

        log.Warnf(st, 
            out.Format("15:04:05"),
            tools.FormatIntComma(status.Parsed), 
            tools.FormatIntComma(status.Skipped),
            tools.FormatIntComma(status.Error),
            tools.FormatIntComma(status.Credential),
            tools.FormatIntComma(status.Url),
            tools.FormatIntComma(status.Email),
        )

        tools.RemoveFolder(tempFolder)

    },
}

func init() {
    parserCmd.AddCommand(intelxCmd)

    intelxCmd.Flags().StringVarP(&intelxCmdOptions.Path, "path", "p", "", "A Path with IntelX file(s).")
}