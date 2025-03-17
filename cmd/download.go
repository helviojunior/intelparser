package cmd

import (

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/spf13/cobra"
)

var downloadProxy string
var downloadCmd = &cobra.Command{
    Use:   "download",
    Short: "Work with intelparser downloaders",
    Long: ascii.LogoHelp(ascii.Markdown(`
# download

Work with intelparser downloaders.
`)),
    PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
        var err error

        // Annoying quirk, but because I'm overriding PersistentPreRun
        // here which overrides the parent it seems.
        // So we need to explicitly call the parent's one now.
        if err = rootCmd.PersistentPreRunE(cmd, args); err != nil {
            return err
        }
        return nil
    },
}

func init() {
    rootCmd.AddCommand(downloadCmd)

    downloadCmd.PersistentFlags().StringVarP(&downloadProxy, "proxy", "X", "", "Proxy to pass traffic through: <scheme://ip:port>")
}
