package cmd

import (
	"bufio"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "strings"
    "regexp"
    "strconv"

    "github.com/helviojunior/intelparser/internal/ascii"
    "github.com/helviojunior/intelparser/pkg/database"
    "github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/pkg/models"
    "github.com/helviojunior/intelparser/pkg/writers"
    "github.com/spf13/cobra"
)

type ConvStatus struct {
    Converted int
    Url int
    Email int
    Credential int
    Label string
}

var rptFilter = ""
var filterList = []string{}
var reportCmd = &cobra.Command{
    Use:   "report",
    Short: "Work with intelparser reports",
    Long: ascii.LogoHelp(ascii.Markdown(`
# report

Work with intelparser reports.
`)),
    PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
        var err error

        // Annoying quirk, but because I'm overriding PersistentPreRun
        // here which overrides the parent it seems.
        // So we need to explicitly call the parent's one now.
        if err = rootCmd.PersistentPreRunE(cmd, args); err != nil {
            return err
        }

        re := regexp.MustCompile("[^a-zA-Z0-9@-_.]")
        s := strings.Split(rptFilter, ",")
        for _, s1 := range s {
            s2 := strings.ToLower(strings.Trim(s1, " "))
            s2 = re.ReplaceAllString(s2, "")
            if s2 != "" {
                filterList = append(filterList, s2)
            }
        }
        
        if len(filterList) > 0 {
            log.Warn("Filter list: " + strings.Join(filterList, ", "))
        }

        return nil
    },
}

func init() {
    rootCmd.AddCommand(reportCmd)

    reportCmd.PersistentFlags().StringVar(&rptFilter, "filter", "", "Comma-separated terms to filter results")
}

func (st *ConvStatus) Print() { 
    switch st.Label {
        case "[=====]":
            st.Label = "[ ====]"
        case  "[ ====]":
            st.Label = "[  ===]"
        case  "[  ===]":
            st.Label = "[=  ==]"
        case "[=  ==]":
            st.Label = "[==  =]"
        case  "[==  =]":
            st.Label = "[===  ]"
        case "[===  ]":
            st.Label = "[==== ]"
        default:
            st.Label = "[=====]"
    }

    fmt.Fprintf(os.Stderr, "%s\n    %s converted %d: cred: %d, url: %d, email: %d\r\033[A", 
        "                                                                        ",
        st.Label, st.Converted, st.Credential, st.Url, st.Email)
    
} 

func containsFilterWord(s string) bool {
    //If filter list is empty, always return true
    if len(filterList) == 0 {
        return true
    }

    s = strings.ToLower(strings.Trim(s, " "))
    if s == "" {
        return false
    }
    for _, f := range filterList {
        if strings.Contains(s, f) {
            return true
        }
    }
    return false
}

func getFilteredOnly(file models.File) *models.File {
    nf := file.Clone()

    for _, c := range file.Credentials {
        if containsFilterWord(c.Username) || containsFilterWord(c.Url) || containsFilterWord(c.Password) || containsFilterWord(c.NearText) {
            nf.Credentials = append(nf.Credentials, c)
        }
    }

    for _, eml := range file.Emails {
        if containsFilterWord(eml.Email) || containsFilterWord(eml.NearText) {
            nf.Emails = append(nf.Emails, eml)
        }
    }

    for _, u := range file.URLs {
        if containsFilterWord(u.Url) || containsFilterWord(u.NearText) {
            nf.URLs = append(nf.URLs, u)
        }
    }

    if !containsFilterWord(nf.Content) && len(nf.Credentials) == 0 && len(nf.Emails) == 0 && len(nf.URLs) == 0 {
        return nil
    }

    return nf
}

func prepareSQL(fields []string) string {
    sql := ""
    for _, f := range fields {
        for _, w := range filterList {
            if sql != "" {
                sql += " or "
            }
            sql += " " + f + " like '%"+ w + "%' "
        }
    }
    return sql
}

func convertFromDbTo(from string, writer writers.Writer, status *ConvStatus) error {
	log.Info("starting conversion...")
    conn, err := database.Connection(fmt.Sprintf("sqlite:///%s", from), true, false)
    if err != nil {
        return err
    }

    //if err := conn.Model(&models.File{}).Preload(clause.Associations).Find(&results).Error; err != nil {
    //    return err
    //}

    rows, err := conn.Model(&models.File{}).Rows()
    defer rows.Close()
    if err != nil {
        return err
    }

    var file models.File
    for rows.Next() {
        conn.ScanRows(rows, &file)

        logger := log.With("id", file.ID, "file", file.FileName)

        sql1 := "file_id == " + strconv.FormatUint(uint64(file.ID), 10)

        sqlCred := sql1 + " and (" + prepareSQL([]string{"username", "url", "password", "near_text"}) + ")"
        rCred, err := conn.Model(&models.Credential{}).Where(sqlCred).Rows()
        defer rCred.Close()
        if err != nil {
            return err
        }

        sqlEmail := sql1 + " and (" + prepareSQL([]string{"email", "near_text"}) + ")"
        rEml, err := conn.Model(&models.Email{}).Where(sqlEmail).Rows()
        defer rEml.Close()
        if err != nil {
            return err
        }

        sqlUrl := sql1 + " and (" + prepareSQL([]string{"url", "near_text"}) + ")"
        rUrl, err := conn.Model(&models.URL{}).Where(sqlUrl).Rows()
        defer rUrl.Close()
        if err != nil {
            return err
        }

        newResult := file.Clone()

        logger.Debug("Checking credentials...")
        var c models.Credential
        for rCred.Next() {
            conn.ScanRows(rCred, &c)
            if containsFilterWord(c.UserDomain) || containsFilterWord(c.Url) || containsFilterWord(c.NearText) {
                newResult.Credentials = append(newResult.Credentials, c)
                status.Credential++
            }
        }

        logger.Debug("Checking emails...")
        var eml models.Email
        for rEml.Next() {
            conn.ScanRows(rEml, &eml)
            if containsFilterWord(eml.Email) || containsFilterWord(eml.NearText) {
                newResult.Emails = append(newResult.Emails, eml)
                status.Email++
            }
        }

        logger.Debug("Checking urls...")
        var u models.URL
        for rUrl.Next() {
            conn.ScanRows(rUrl, &u)
            if containsFilterWord(u.Url) || containsFilterWord(u.NearText) {
                newResult.URLs = append(newResult.URLs, u)
                status.Url++
            }
        }

        if containsFilterWord(newResult.Content) || len(newResult.Credentials) != 0 || len(newResult.Emails) != 0 || len(newResult.URLs) != 0 {
            logger.Debug("Converting file!")
            status.Converted++
            if err := writer.Write(newResult); err != nil {
                return err
            }
        }
    }

    return nil
}

func convertFromJsonlTo(from string, writer writers.Writer, status *ConvStatus) error {
	log.Info("starting conversion...")

    file, err := os.Open(from)
    if err != nil {
        return err
    }
    defer file.Close()

    reader := bufio.NewReader(file)
    for {
        line, err := reader.ReadBytes('\n')
        if err != nil {
            if err == io.EOF {
                if len(line) == 0 {
                    break // End of file
                }
                // Handle the last line without '\n'
            } else {
                return err
            }
        }

        var result models.File
        if err := json.Unmarshal(line, &result); err != nil {
            log.Error("could not unmarshal JSON line", "err", err)
            continue
        }

        newResult := getFilteredOnly(result)
        if newResult != nil {
            if err := writer.Write(newResult); err != nil {
                return err
            }
            status.Converted++
            status.Url += len(newResult.URLs)
            status.Email += len(newResult.Emails)
            status.Credential += len(newResult.Credentials)
        }

        if err == io.EOF {
            break
        }
    }

    return nil
}