package rules

import (
    re "regexp"
    "strings"
    "time"

    "github.com/helviojunior/intelparser/pkg/models"
)

// Document extracts Brazilian CPF and CNPJ numbers, formatted or glued, into a
// single index distinguished by the is_cpf / is_cnpj flags.
//
// Both documents carry check digits, so the regex only proposes candidates and
// the PostProcessor confirms them with the official checksum. That makes the
// validation extremely strong: random 11/14 digit runs almost never pass, so
// the false-positive rate is low even on the glued (unformatted) variants.
func Document() *Rule {
    r := &Rule{
        RuleID:      "Document",
        Description: "Extract Brazilian CPF and CNPJ numbers.",
        // Alternatives:
        //   1) CNPJ formatted: 12.345.678/0001-95
        //   2) CPF  formatted: 123.456.789-09
        //   3) CNPJ glued: 14-digit run (\b bounded)
        //   4) CPF  glued: 11-digit run (\b bounded)
        Regex: re.MustCompile(
            `\d{2}\.\d{3}\.\d{3}/\d{4}-\d{2}` +
                `|\d{3}\.\d{3}\.\d{3}-\d{2}` +
                `|\b\d{14}\b` +
                `|\b\d{11}\b`),
        Entropy:             0,
        Keywords:            []string{},
        CheckGlobalStopWord: false,
        PostProcessor: func(finding *models.Finding) (bool, error) {
            raw := strings.TrimSpace(finding.Secret)
            digits := nonDigit.ReplaceAllString(raw, "")

            var isCPF, isCNPJ bool
            switch len(digits) {
            case 11:
                if !isValidCPF(digits) {
                    return false, nil
                }
                isCPF = true
            case 14:
                if !isValidCNPJ(digits) {
                    return false, nil
                }
                isCNPJ = true
            default:
                return false, nil
            }

            finding.Document = models.Document{
                Time:   time.Now(),
                Raw:    raw,
                Number: digits,
                IsCPF:  isCPF,
                IsCNPJ: isCNPJ,
            }
            return true, nil
        },
    }

    return r
}

// isValidCNPJ reports whether a 14-digit string is a valid Brazilian CNPJ
// (matching both check digits). Sequences of identical digits are rejected.
func isValidCNPJ(d string) bool {
    if len(d) != 14 {
        return false
    }
    allEqual := true
    for i := 1; i < 14; i++ {
        if d[i] != d[0] {
            allEqual = false
            break
        }
    }
    if allEqual {
        return false
    }

    weights1 := []int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}
    weights2 := []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}

    calc := func(weights []int) int {
        sum := 0
        for i, w := range weights {
            sum += int(d[i]-'0') * w
        }
        r := sum % 11
        if r < 2 {
            return 0
        }
        return 11 - r
    }

    return calc(weights1) == int(d[12]-'0') && calc(weights2) == int(d[13]-'0')
}
