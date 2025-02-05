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
        Regex:       re.MustCompile(`` +
                        `(?i)` +  // Case-insensitive matching
                        `([a-zA-Z0-9_\-\.]+` +  // Escaped characters in local part
                        `[@|%40]` +  // Separator
                        `[A-Z0-9](?:[A-Z0-9-]*[A-Z0-9])?` +  // Domain name
                        `\.(?:[A-Z0-9](?:[A-Z0-9-]*[A-Z0-9])?)+)` +  // Top-level domain and subdomains
                ``),
        Entropy:     2.1,
        Keywords:    []string{"@", "%40"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            var m *mail.Address
            var err error

            e1 := strings.Trim(finding.Secret, ". ")
            if m, err = mail.ParseAddress(strings.Replace(e1, "%40", "@", -1)); err != nil {
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
