package rules

import (
    re "regexp"
    "time"
    "net/mail"
    "net/url"
    "strings"
    //"fmt"
    //"errors"

    "github.com/helviojunior/intelparser/pkg/models"
)

func Leak3() *Rule {
    var iRe = re.MustCompile(`(?i)(https?:\/\/[a-zA-Z0-9.-]+(?:\.[^\x00-\x1F\s\\,"'<: ]{2,})(?:\/[^\x00-\x1F\s\\,"'<: ]*)?)[: ]{1,3}([a-z0-9._-]+(@|%40)[a-z0-9.-]+\.[a-z]{2,}):([^\s\\]{3,})`)
    // define rule
    r := &Rule{
        RuleID:      "Leak3 » URL:Email:Pass",
        Description: "Extract URL:Email:Pass leaks",
        Regex:       iRe,
        Entropy:     0.91,
        SecretGroup: 4,
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
            if len(groups) >= 4 {
               u1 = groups[1]
               u2 = groups[2]
               p1 = groups[4]
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