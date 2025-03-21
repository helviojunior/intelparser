package log

import (
    "os"
    "fmt"
    //"io"
    "bufio"
    "runtime"
    

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/charmbracelet/lipgloss"
    "github.com/charmbracelet/log"
    "github.com/muesli/termenv"
    //"golang.org/x/sys/unix"
)

// LLogger is a charmbracelet logger type redefinition
type LLogger = log.Logger

// Logger is this package level logger
var Logger *LLogger
var logFilePath string
var bl string

func init() {
    styles := log.DefaultStyles()
    styles.Keys["err"] = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
    styles.Values["err"] = lipgloss.NewStyle().Bold(true)
    profile := termenv.EnvColorProfile()

    if runtime.GOOS == "windows" {
        bl = "\r\n"
    }else{
        bl = "\n"
    }

    r, w, _ := os.Pipe()

    go func() {
        f := bufio.NewWriter(os.Stdout)
        defer f.Flush()
        
        scanner := bufio.NewScanner(r)
        for scanner.Scan() {
            s := scanner.Text() + bl
            clearLine()
            //os.Stdout.WriteString(s)
            f.WriteString(s)
            f.Flush()
            writeTextToFile(ascii.ScapeAnsi(s))
        }
    }()

    Logger = log.NewWithOptions(w, log.Options{
        ReportTimestamp: false,
    })
    Logger.SetStyles(styles)
    Logger.SetLevel(log.InfoLevel)
    Logger.SetColorProfile(profile)

}

func SetOutFile(destination string) error{
    logFilePath = destination

    // Open the file in append mode, create it if it doesn't exist
    file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return err
    }
    defer file.Close()

    if _, err := file.WriteString(ascii.LogoHelp("")); err != nil {
        return err
    }

    return nil
}

func writeDataToFile(data []byte) {
    if logFilePath == "" {
        return
    }

    // Open the file in append mode, create it if it doesn't exist
    file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer file.Close()

    file.Write(data)

}

func writeTextToFile(msg string) {
    if logFilePath == "" {
        return
    }

    // Open the file in append mode, create it if it doesn't exist
    file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer file.Close()

    file.WriteString(msg)

}

// EnableDebug enabled debug logging and caller reporting
func EnableDebug() {
    Logger.SetLevel(log.DebugLevel)
    Logger.SetReportCaller(true)
}

// EnableSilence will silence most logs, except this written with Print
func EnableSilence() {
    Logger.SetLevel(log.FatalLevel + 100)
}

// Debug logs debug messages
func Debug(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Debug(msg, keyvals...)
}
func Debugf(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Debug(fmt.Sprintf(format, a...) )
}

// Info logs info messages
func Info(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Info(msg, keyvals...)
}
func Infof(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Info(fmt.Sprintf(format, a...) )
}


// Warn logs warning messages
func Warn(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Warn(msg, keyvals...)
}
func Warnf(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Warn(fmt.Sprintf(format, a...) )
}


// Error logs error messages
func Error(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Error(msg, keyvals...)
}
func Errorf(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Error(fmt.Sprintf(format, a...) )
}

// Fatal logs fatal messages and panics
func Fatal(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Fatal(msg, keyvals...)
}
func Fatalf(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Fatal(fmt.Sprintf(format, a...) )
}


// Print logs messages regardless of level
func Print(msg string, keyvals ...interface{}) {
    Logger.Helper()
    Logger.Print(msg, keyvals...)
}
func Printf(format string, a ...interface{}) {
    Logger.Helper()
    Logger.Print(fmt.Sprintf(format, a...) )
}

// With returns a sublogger with a prefix
func With(keyvals ...interface{}) *LLogger {
    return Logger.With(keyvals...)
}