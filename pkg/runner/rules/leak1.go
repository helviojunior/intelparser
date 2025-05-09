package rules

import (
    re "regexp"
    "time"
    "net/mail"
    "strings"

    "github.com/helviojunior/intelparser/pkg/models"
)

func Leak1() *Rule {
    // define rule
    r := &Rule{
        RuleID:      "Leak1 » Email:Pass",
        Description: "Extract Email:Pass leaks",
        Regex:       re.MustCompile(`(?i)([a-z0-9._-]+(@|%40)[a-z0-9.-]+\.[a-z]{2,}):([^\s\\]{3,})`),
        Entropy:     0.91,
        SecretGroup: 3,
        Keywords:    []string{"@", ":"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            var m *mail.Address
            var err error

            s1 := strings.SplitN(finding.Match, ":", 2)
            if !strings.Contains(s1[0], "@") {
                return false, nil
            }

            e1 := strings.ToLower(strings.Replace(strings.Trim(s1[0], ". "), "%40", "@", -1))
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

            finding.Credential = models.Credential{
                Time        : time.Now(),
                UserDomain  : finding.Email.Domain,
                Username    : m.Address,
                Password    : s1[1],
                Url         : "",
                UrlDomain   : "",
                Severity    : 100,
                Entropy     : finding.Entropy,
            }
            return true, nil
        },
    }

    return r
}