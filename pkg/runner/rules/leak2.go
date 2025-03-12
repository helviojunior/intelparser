package rules

import (
    re "regexp"
    "time"
    "net/mail"
    "net/url"
    "strings"
    //"fmt"

    "github.com/helviojunior/intelparser/pkg/models"
)

func Leak2() *Rule {
    // define rule
    r := &Rule{
        RuleID:      "Leak2 » URL:Email:Pass",
        Description: "Extract Email:Pass leaks",
        Regex:       re.MustCompile(`(?i)([a-zA-Z0-9_]+)[: ]{1,3}([a-zA-Z0-9_-]{2,30}:\/\/[^\"'\n]{1,512})\n[ \t]{0,5}(user|username|login|email)[ :]{1,3}([^\n]{3,512})\n[ \t]{0,5}(pass|password|token|secret|senha|pwd)[ :]{1,3}([^\n\r\t]{3,512})`),
        Entropy:     0.91,
        SecretGroup: 6,
        Keywords:    []string{"http://", "https://"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            
            var err error
            var m *mail.Address
            var u1 string
            var u2 string
            var p1 string
            var d1 string
 
            for _, p := range strings.Split(finding.Match, "\n") {

                s1 := strings.SplitN(p, ":", 2)
                k := strings.ToLower(strings.Trim(s1[0], " \r\n\t"))
                v := strings.Trim(s1[1], " \r\n\t")

                switch k {
                case "url", "host":
                u1 = v

                case "user", "username", "login", "email":
                u2 = v

                case "pass", "password", "token", "secret", "senha", "pwd":
                p1 = v
                }
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

            finding.Credential = models.Credential{
                Time        : time.Now(),
                UserDomain  : d1,
                UrlDomain   : finding.Url.Domain,
                Username    : u2,
                Password    : p1,
                Url         : u1,
                Severity    : 100,
                Entropy     : finding.Entropy,
                NearText    : finding.Match,
            }
            return true, nil
        },
    }

    return r
}