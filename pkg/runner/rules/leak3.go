package rules

import (
    re "regexp"
    "time"
    "net/mail"
    "net/url"
    "strings"
    //"fmt"
    "errors"

    //"github.com/helviojunior/intelparser/pkg/log"
    "github.com/helviojunior/intelparser/internal/tools"
    "github.com/helviojunior/intelparser/pkg/models"
)

func Leak3() *Rule {
    var iRe = re.MustCompile(`(?i)(https?:\/\/[a-zA-Z0-9.-]+(?:\.[^\x00-\x1F\s\\,"'<: ]{2,})(?::[0-9]{2,5})?(?:\/[^\x00-\x1F\s\\,"'<: ]*)?)[: ]{1,3}([a-z0-9.\\@%_-]{3,}):([^\s\\]{3,})`)
    
    
    // define rule
    r := &Rule{
        RuleID:      "Leak3 » URL:User:Pass",
        Description: "Extract URL:User:Pass leaks",
        Regex:       iRe,
        Entropy:     0.91,
        SecretGroup: 3,
        Keywords:    []string{"http://", "https://"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            
            var err error
            var m *mail.Address
            var u1 string
            var u2 string
            var p1 string
            var d1 string

            groups := iRe.FindStringSubmatch(finding.Line)
            if len(groups) >= 3 {
               u1 = strings.Trim(groups[1], "\r\n ")
               u2 = strings.Trim(groups[2], "\r\n ")
               p1 = groups[3]
            }

            if tools.SliceHasStr([]string{"http", "https", "include", "ftp"}, strings.ToLower(u2)){
                return false, errors.New("Invalid submatch.")
            }

            if strings.Contains(strings.ToLower(u2), "http") {
                return false, errors.New("Invalid submatch.")
            }

            if strings.Contains(u2, "@") {

                e1 := strings.ToLower(strings.Replace(strings.Trim(u2, ". "), "%40", "@", -1))
                e1 = strings.Replace(e1, ".@", "@", -1)
                e1 = strings.Replace(e1, "@.", "@", -1)
                if m, err = mail.ParseAddress(e1); err != nil {
                    return false, err
                }

                finding.Email = models.Email{
                    Time        : time.Now(),
                    Domain      : strings.Split(m.Address, "@")[1],
                    Email       : m.Address,
                }

                u2 = m.Address
                d1 = finding.Email.Domain

            }else if strings.Contains(u2, "\\") {
                e1 := strings.SplitN(u2, "\\", 2)
                if e1[0] != "" && e1[1] != "" {
                    d1 = e1[0]
                }
            }

            u1 = strings.Replace(u1, "http://http://", "http://", -1)
            u1 = strings.Replace(u1, "https://https://", "https://", -1)
            u1 = strings.Replace(u1, "http://https://", "https://", -1)
            u1 = strings.Replace(u1, "https://http://", "http://", -1)
            u1 = strings.Replace(u1, "http://http:", "http://", -1)
            u1 = strings.Replace(u1, "https://https", "https://", -1)

            u, err := url.Parse(u1)
            if err != nil {
                return false, err
            }

            finding.Url = models.URL{
                Time        : time.Now(),
                Domain      : strings.ToLower(u.Hostname()),
                Url         : u1,
            }

            hasCpf := false
            cpf := ""
            if ok, c := tools.ExtractCPF(u2); ok {
                hasCpf = true
                cpf = c
            }
            if ok, c := tools.ExtractCPF(p1); ok {
                hasCpf = true
                cpf = c
            }

            finding.Credential = models.Credential{
                Time        : time.Now(),
                UserDomain  : d1,
                UrlDomain   : finding.Url.Domain,
                Username    : u2,
                Password    : p1,
                Url         : u1,
                Severity    : 100,
                Entropy     : finding.Entropy,
                HasCPF      : hasCpf,
                CPF         : cpf,
            }
            return true, nil
        },
    }

    return r
}