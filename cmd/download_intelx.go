package cmd

import (

    "errors"
    "fmt"
    "time"
    "os"

    "github.com/gofrs/uuid"
    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/internal/islazy"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/downloaders"
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

        return nil
    },
    Run: func(cmd *cobra.Command, args []string) {

        zipFile, err := islazy.ResolveFullPath(fmt.Sprintf("./ix_%s_%s.zip", islazy.SafeFileName(searchTerm), startTime.Format("2006-02-01_15-04-05")))
        if err != nil {
            log.Error("Error setting output file", "err", err)
            os.Exit(2)
        }

        dwn, err := downloaders.NewIntelXDownloader(searchTerm, ixApiKey, zipFile)
        if err != nil {
            log.Error("Error getting downloader instance", "err", err)
            os.Exit(2)
        }

        dwn.ProxyURL = downloadProxy

        status := dwn.Run()
        dwn.Close()

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
            islazy.FormatInt64Comma(status.TotalBytes),
        )

    },
}

func init() {
    downloadCmd.AddCommand(dwnIXCmd)

    dwnIXCmd.Flags().StringVar(&searchTerm, "term", "", "Search term to performs a search and queries the results.")
    dwnIXCmd.Flags().StringVar(&ixApiKey, "api-key", "", "IntelX API Key. You can also provide API Key using Environment Variable 'IXAPIKEY'.")
    
}
