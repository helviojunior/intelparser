package rules

import (
    re "regexp"
    "time"
    "net/url"
    "strings"

    "github.com/helviojunior/intelparser/pkg/models"
)

func Url() *Rule {
    // define rule
    r := &Rule{
        RuleID:      "Url",
        Description: "Extract URLs.",
        Regex:       re.MustCompile(`(?i)(https?://[^\s,"'<]+/[^\s,"'<: ]+)`),
        Entropy:     3,
        Keywords:    []string{"http://", "https://"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {

            u, err := url.Parse(finding.Secret)
            if err != nil {
                return false, err
            }

            finding.Url = models.URL{
                Time        : time.Now(),
                Domain      : strings.ToLower(u.Hostname()),
                Url         : finding.Secret,
            }
            return true, nil
        },
    }

    return r
}