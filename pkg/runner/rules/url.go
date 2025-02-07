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

            u1 := finding.Secret
            u1 = strings.Replace(u1, "http://http://", "http://", -1)
            u1 = strings.Replace(u1, "https://https://", "https://", -1)
            u1 = strings.Replace(u1, "http://https://", "https://", -1)
            u1 = strings.Replace(u1, "https://http://", "http://", -1)

            u, err := url.Parse(u1)
            if err != nil {
                return false, err
            }

            finding.Url = models.URL{
                Time        : time.Now(),
                Domain      : strings.ToLower(u.Hostname()),
                Url         : u1,
            }
            return true, nil
        },
    }

    return r
}