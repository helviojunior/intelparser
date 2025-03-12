package rules

import (
    re "regexp"
    "time"
    "net/mail"
    "strings"

    "github.com/helviojunior/intelparser/pkg/models"
)

func Email() *Rule {
    // define rule
    r := &Rule{
        RuleID:      "Email",
        Description: "Extract Emails.",
        Regex:       re.MustCompile(`(?i)(\b[a-z0-9._-]+(@|%40)[a-z0-9.-]+\.[a-z]{2,})`),
        Entropy:     2.1,
        Keywords:    []string{"@", "%40"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            var m *mail.Address
            var err error

            e1 := strings.Trim(finding.Secret, ". ")
            e1 = strings.Replace(e1, "%40", "@", -1)
            e1 = strings.Replace(e1, ".@", "@", -1)
            e1 = strings.Replace(e1, "@.", "@", -1)
            if m, err = mail.ParseAddress(e1); err != nil {
                return false, err
            }

            finding.Email = models.Email{
                Time        : time.Now(),
                Domain      : strings.SplitN(m.Address, "@", 2)[1],
                Email       : m.Address,
            }
            return true, nil
        },
    }

    return r
}
