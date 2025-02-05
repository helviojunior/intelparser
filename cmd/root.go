package cmd

import (
	//"crypto/tls"
	//"net/http"
	"os/user"
	"os"
	"fmt"

	"github.com/helviojunior/intelparser/internal/ascii"
	"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/runner"
	"github.com/spf13/cobra"
)

var (
	opts = &runner.Options{}
)

var rootCmd = &cobra.Command{
	Use:   "intelparser",
	Short: "intelparser is a modular password sprayer",
	Long:  ascii.Logo(),
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		
		usr, err := user.Current()
	    if err != nil {
	       return err
	    }

	    opts.Writer.UserPath = usr.HomeDir

		if opts.Logging.Silence {
			log.EnableSilence()
		}

		if opts.Logging.Debug && !opts.Logging.Silence {
			log.EnableDebug()
			log.Debug("debug logging enabled")
		}
		return nil
	},
}

func Execute() {
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.SilenceErrors = true
	err := rootCmd.Execute()
	if err != nil {
		var cmd string
		c, _, cerr := rootCmd.Find(os.Args[1:])
		if cerr == nil {
			cmd = c.Name()
		}

		v := "\n"

		if cmd != "" {
			v += fmt.Sprintf("An error occured running the `%s` command\n", cmd)
		} else {
			v += "An error has occured. "
		}

		v += "The error was:\n\n" + fmt.Sprintf("```%s```", err)
		fmt.Println(ascii.Markdown(v))

		os.Exit(1)
	}
}

func init() {
	// Disable Certificate Validation (Globally)
	//http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Debug, "debug-log", "D", false, "Enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Silence, "quiet", "q", false, "Silence (almost all) logging")
	
}
