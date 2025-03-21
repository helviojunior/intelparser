package cmd

import (
	//"crypto/tls"
	//"net/http"
	"os/user"
	"os"
	"fmt"
	"time"
	"os/signal"
    "syscall"

	"github.com/helviojunior/intelparser/internal/islazy"
	"github.com/helviojunior/intelparser/internal/ascii"
	"github.com/helviojunior/intelparser/pkg/log"
	"github.com/helviojunior/intelparser/pkg/runner"
	"github.com/spf13/cobra"
)

var (
	opts = &runner.Options{}
)

var startTime time.Time
var rootCmd = &cobra.Command{
	Use:   "intelparser",
	Short: "intelparser is a modular Intel/Leaks parser",
	Long:  ascii.Logo(),
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {

        startTime = time.Now()

		usr, err := user.Current()
	    if err != nil {
	       return err
	    }

	    opts.Writer.UserPath = usr.HomeDir

	    if cmd.CalledAs() != "version" {
			fmt.Println(ascii.Logo())
		}

		if opts.Logging.Silence {
			log.EnableSilence()
		}

		if opts.Logging.Debug && !opts.Logging.Silence {
			log.EnableDebug()
			log.Debug("debug logging enabled")
		}

		if opts.Logging.TextFile != "" {
			// check if the destination exists, if not, create it
		    dst, err := islazy.CreateFileWithDir(opts.Logging.TextFile)
		    if err != nil {
		        return err
		    }
		    opts.Logging.TextFile = dst

			err = log.SetOutFile(opts.Logging.TextFile)
			if err != nil {
		       return err
		    }
		}

		return nil
	},
}

func Execute() {
	c := make(chan os.Signal)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-c
        ascii.ClearLine()
        fmt.Fprintf(os.Stderr, "\r\n")
        ascii.ClearLine()
        ascii.ShowCursor()
        log.Warn("interrupted, shutting down...                            ")
        ascii.ClearLine()
        fmt.Printf("\n")
        os.Exit(2)
    }()

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

	//Time to wait the logger flush
	time.Sleep(time.Second)
}

func init() {
	// Disable Certificate Validation (Globally)
	//http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Debug, "debug-log", "D", false, "Enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Silence, "quiet", "q", false, "Silence (almost all) logging")

	rootCmd.PersistentFlags().StringVarP(&opts.Logging.TextFile, "write-text-file", "o", "", "The file to write Text lines to")
	
}
