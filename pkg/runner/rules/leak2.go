package rules

import (
    re "regexp"

    "github.com/helviojunior/intelparser/pkg/models"
)

func UrlEmailPass() *Rule {
    // define rule
    r := &Rule{
        RuleID:      "URL:Email:Pass",
        Description: "Extract Email:Pass leaks",
        Regex:       re.MustCompile(`(?i)(?m)([a-zA-Z0-9_]+)[: ]{1,3}([a-zA-Z0-9_-]{2,30}:\/\/[^\"'\n]{1,512})\n[ \t]{0,5}(user|username|login|email)[ :]{1,3}([^\n]{3,512})\n[ \t]{0,5}(pass|password|token|secret|senha|pwd)[ :]{1,3}([^\n]{3,512})`),
        Entropy:     3,
        Keywords:    []string{"http://", "https://"},
        CheckGlobalStopWord: false,
        PostProcessor : func(finding *models.Finding) (bool, error) {
            
            return false, nil
        },
    }

    return r
}