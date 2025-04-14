package cmd

import (

    "errors"
    "fmt"
    "time"
    "os"
    "sync"

    "github.com/gofrs/uuid"
    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/islazy"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/downloaders"
    "github.com/helviojunior/intelparser/pkg/readers"
    resolver "github.com/helviojunior/gopathresolver"
    "github.com/spf13/cobra"
)

var searchTerm string
var ixApiKey string
var dwnIXCmd = &cobra.Command{
    Use:   "intelx",
    Short: "Search and Download from IntelX.io",
    Long: ascii.LogoHelp(ascii.Markdown(`
# download intelx

Search and Download from IntelX.io.

An IntelX API key must be provided. You can specify it using the --api-key parameter in the command line 
or by setting the IXAPIKEY environment variable.
`)),
    Example: `
   - intelparser download intelx --term sec4us.com.br
   - intelparser download intelx --term "~/Desktop/term_list.txt"
   - intelparser download intelx --term sec4us.com.br --api-key 00000000-0000-0000-0000-000000000000

   Terms types supported:
   * Email address
   * Domain, including wildcards like *.example.com
   * URL
   * IPv4 and IPv6
   * CIDRv4 and CIDRv6
   * Phone Number
   * Bitcoin address
   * MAC address
   * IPFS Hash
   * UUID
   * Simhash
   * Credit card number
   * IBAN
   `,
    PreRunE: func(cmd *cobra.Command, args []string) error {
        var err error

        if ixApiKey == "" {
            eKey := os.Getenv("IXAPIKEY")
            if eKey == "" {
                return errors.New("IntelX API key not provided. You can specify it using the --api-key parameter in the command line or by setting the IXAPIKEY environment variable.")
            }

            ixApiKey = eKey
        }
        
        _, err = uuid.FromString(ixApiKey)
        if err != nil {
            return err
        }

        if searchTerm == "" {
            return errors.New("Search term not set")
        }

        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {

        termList := []string{}
        termChan := make(chan string)
        wg := sync.WaitGroup{}
        status := &downloaders.IntelXDownloaderStatus{
          TotalFiles    : 0,
          Downloaded    : 0,
          Duplicated    : 0,
          TotalBytes    : 0,
        }

        // Check if search term is a file path
        fileTerm, err := resolver.ResolveFullPath(searchTerm)
        if err == nil {
            
          ft, err := islazy.FileType(fileTerm)
          if err == nil {
              if ft != "file" {
                 log.Error("Search term must be a single text or file path")
                 os.Exit(2)
              }

              err = readers.ReadFileList(fileTerm, &termList)
          }else{
            termList = append(termList, searchTerm)
          }
        }else{
          termList = append(termList, searchTerm)
        }

        if len(termList) == 0 {
            log.Error("Search term not set")
            os.Exit(2)
        }

        go func() {
            defer close(termChan)
            for _, t := range termList {
                termChan <- t
            }
        
        }()

        wg.Add(1)
        go func() {
          defer wg.Done()
          for true {
            term, ok := <-termChan
            if !ok {
              return
            }

            log.Infof("Quering term %s", term)

            zipFile, err := resolver.ResolveFullPath(fmt.Sprintf("./ix_%s_%s.zip", islazy.SafeFileName(term), startTime.Format("2006-02-01_15-04-05")))
            if err != nil {
                log.Error("Error setting output file", "err", err)
                os.Exit(2)
            }

            dwn, err := downloaders.NewIntelXDownloader(term, ixApiKey, zipFile)
            if err != nil {
                log.Error("Error getting downloader instance", "err", err)
                os.Exit(2)
            }

            dwn.ProxyURL = downloadProxy

            st := dwn.Run()
            dwn.Close()

            status.TotalFiles += st.TotalFiles
            status.Duplicated += st.Duplicated
            status.Downloaded += st.Downloaded
            status.TotalBytes += st.TotalBytes

          }
        }()

        wg.Wait()

        diff := time.Now().Sub(startTime)
        out := time.Time{}.Add(diff)

        st := "Download status\n"
        st += "     -> Elapsed time.....: %s\n"
        st += "     -> Listed files.....: %s\n"
        st += "     -> Duplicated Files.: %s\n"
        st += "     -> Downloaded Files.: %s\n"
        st += "     -> Bytes downloaded.: %s\n"

        log.Infof(st, 
            out.Format("15:04:05"),
            islazy.FormatIntComma(status.TotalFiles), 
            islazy.FormatIntComma(status.Duplicated),
            islazy.FormatIntComma(status.Downloaded),
            islazy.Bytes(uint64(status.TotalBytes)),
        )

    },
}

func init() {
    downloadCmd.AddCommand(dwnIXCmd)

    dwnIXCmd.Flags().StringVar(&searchTerm, "term", "", "Search term (or filename with terms) to performs a search and queries the results.")
    dwnIXCmd.Flags().StringVar(&ixApiKey, "api-key", "", "IntelX API Key. You can also provide API Key using Environment Variable 'IXAPIKEY'.")
    
}
