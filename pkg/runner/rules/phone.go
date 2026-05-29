package rules

import (
    re "regexp"
    "strings"
    "time"

    "github.com/helviojunior/intelparser/pkg/models"
)

// nonDigit strips everything that is not 0-9 from a candidate so the
// validators below can reason about the bare numeric payload.
var nonDigit = re.MustCompile(`[^0-9]`)

// digitGroups splits a candidate into its runs of digits, preserving order.
// The size of the area-code group is the key signal used to disambiguate a
// bare 10-digit number (a 3-digit group is a US area code, a 2-digit group is
// a Brazilian DDD).
var digitGroups = re.MustCompile(`\d+`)

// validBrDDD holds every area code (DDD) currently assigned in Brazil.
// Matching against the real list (instead of "any two digits") is the single
// most effective filter to keep random 10/11 digit numbers out of the results.
var validBrDDD = map[string]struct{}{
    "11": {}, "12": {}, "13": {}, "14": {}, "15": {}, "16": {}, "17": {}, "18": {}, "19": {},
    "21": {}, "22": {}, "24": {}, "27": {}, "28": {},
    "31": {}, "32": {}, "33": {}, "34": {}, "35": {}, "37": {}, "38": {},
    "41": {}, "42": {}, "43": {}, "44": {}, "45": {}, "46": {}, "47": {}, "48": {}, "49": {},
    "51": {}, "53": {}, "54": {}, "55": {},
    "61": {}, "62": {}, "63": {}, "64": {}, "65": {}, "66": {}, "67": {}, "68": {}, "69": {},
    "71": {}, "73": {}, "74": {}, "75": {}, "77": {}, "79": {},
    "81": {}, "82": {}, "83": {}, "84": {}, "85": {}, "86": {}, "87": {}, "88": {}, "89": {},
    "91": {}, "92": {}, "93": {}, "94": {}, "95": {}, "96": {}, "97": {}, "98": {}, "99": {},
}

// Phone extracts Brazilian and US phone numbers written in the many common
// formattings (with/without +55 or +1, with/without parenthesised area code,
// with spaces, dots or dashes, and the fully glued "no separator" variant).
//
// The regex is intentionally only a candidate detector: it grabs phone-shaped
// spans and the PostProcessor performs the strict numbering-plan validation
// (real BR DDDs, NANP rules, subscriber length/leading-digit). The Secret that
// reaches the PostProcessor is re-parsed from its bare digits, so whatever the
// regex happened to capture into groups is irrelevant for classification.
func Phone() *Rule {
    r := &Rule{
        RuleID:      "Phone",
        Description: "Extract Brazilian and US phone numbers.",
        // Alternatives, all bounded by \b so we never grab the tail of a longer
        // digit run (IDs, card numbers, dates, ...):
        //   1) +55 / +1 / 0055 / 001 with an explicit country code
        //   2) parenthesised area code: (11) 99999-9999 / (415) 555-2671
        //   3) separated groups without country: 11 99999-9999 / 415-555-2671
        //   4) fully glued 10-13 digit run: 11987654321 / 5511987654321
        Regex: re.MustCompile(
            `(?:\+|00)?(?:55|1)[\s.\-]\(?\d{2,3}\)?[\s.\-]?\d{3,5}[\s.\-]?\d{4}` +
                `|\(\d{2,3}\)[\s.\-]?\d{3,5}[\s.\-]?\d{4}` +
                `|\b\d{2,3}[\s.\-]\d{3,5}[\s.\-]?\d{4}\b` +
                `|\+?\b\d{10,13}\b`),
        Entropy:             0,
        Keywords:            []string{},
        CheckGlobalStopWord: false,
        PostProcessor: func(finding *models.Finding) (bool, error) {
            raw := strings.TrimSpace(finding.Secret)
            hasPlus := strings.HasPrefix(raw, "+")

            digits := nonDigit.ReplaceAllString(raw, "")

            // Drop a "00" international call prefix (e.g. 0055..., 001...).
            if strings.HasPrefix(digits, "00") {
                digits = digits[2:]
                hasPlus = true
            }

            // Length of the area-code group as written, used only to break the
            // bare 10-digit BR/US tie. 0 means "glued / unknown".
            areaLen := 0
            // explicitIntl is true when the candidate carries an unmistakable
            // international country code (a leading "+" or a "1"/"55" written as
            // its own group). It guards the US 11-digit path so that a glued
            // 11-digit run starting with 1 — overwhelmingly a Brazilian CPF in
            // this kind of data set — is never mistaken for a +1 US number.
            explicitIntl := hasPlus
            groups := digitGroups.FindAllString(raw, -1)
            glued := !hasPlus && len(groups) == 1
            if len(groups) >= 2 {
                idx := 0
                // An explicit +55 / +1 country code is its own leading group;
                // skip it so the next group is the real area code.
                if groups[0] == "55" || groups[0] == "1" {
                    explicitIntl = true
                    idx = 1
                }
                if idx < len(groups) {
                    areaLen = len(groups[idx])
                }
            }

            // A glued 11-digit run that is a valid CPF is a Brazilian tax ID, not
            // a phone. CPFs are never written with phone formatting, so we only
            // discard the unformatted case to avoid dropping real numbers that
            // merely happen to share a CPF's check digits.
            if glued && len(digits) == 11 && isValidCPF(digits) {
                return false, nil
            }

            country, formatted, ok := classifyPhone(digits, areaLen, explicitIntl)
            if !ok {
                return false, nil
            }

            finding.Phone = models.Phone{
                Time:    time.Now(),
                Country: country,
                Raw:     raw,
                Phone:   formatted,
            }
            return true, nil
        },
    }

    return r
}

// classifyPhone validates a bare-digit string against the Brazilian and US
// numbering plans and returns the ISO country ("BR"/"US") plus the number
// normalised to international form (digits only, including the country code).
//
// A bare 10/11 digit number is genuinely ambiguous between a Brazilian number
// (DDD + subscriber) and a US one (+1 dropped). Because this is a Brazil-centric
// tool, the data set is overwhelmingly Brazilian, so we prefer the BR plan
// whenever the digits form a valid Brazilian number and only fall back to the
// strict NANP (US) plan otherwise. Numbers carrying an explicit country code
// (+55 / +1) are never ambiguous and are decided by that code.
func classifyPhone(d string, areaLen int, explicitIntl bool) (country string, formatted string, ok bool) {
    // Explicit BR country code: 55 + (10 landline | 11 mobile) national digits.
    if strings.HasPrefix(d, "55") && (len(d) == 12 || len(d) == 13) {
        if national, valid := validBrNational(d[2:]); valid {
            return "BR", "55" + national, true
        }
    }

    switch len(d) {
    case 11:
        // BR mobile (DDD + 9 digits) takes precedence; otherwise a leading 1
        // marks a US number written with its +1 country code — but only when an
        // explicit international prefix was present, otherwise an 11-digit run
        // starting with 1 is far more likely a Brazilian CPF than a US phone.
        if national, valid := validBrNational(d); valid {
            return "BR", "55" + national, true
        }
        if explicitIntl && strings.HasPrefix(d, "1") && validUSNational(d[1:]) {
            return "US", "1" + d[1:], true
        }
    case 10:
        // A 3-digit area-code group is a US fingerprint; a 2-digit group (or a
        // glued number, in this Brazil-centric tool) defaults to BR.
        if areaLen == 3 {
            if validUSNational(d) {
                return "US", "1" + d, true
            }
            if national, valid := validBrNational(d); valid {
                return "BR", "55" + national, true
            }
        } else {
            if national, valid := validBrNational(d); valid {
                return "BR", "55" + national, true
            }
            if validUSNational(d) {
                return "US", "1" + d, true
            }
        }
    }

    return "", "", false
}

// validBrNational validates the national part (DDD + subscriber) of a Brazilian
// number: a valid DDD followed by either a 9-digit mobile (starting with 9) or
// an 8-digit landline (starting with 2-5).
func validBrNational(d string) (string, bool) {
    if len(d) != 10 && len(d) != 11 {
        return "", false
    }
    ddd := d[:2]
    if _, found := validBrDDD[ddd]; !found {
        return "", false
    }
    sub := d[2:]
    if len(sub) == 9 { // mobile
        if sub[0] != '9' {
            return "", false
        }
        return d, true
    }
    // landline (8 digits) — first digit 2..5
    if sub[0] < '2' || sub[0] > '5' {
        return "", false
    }
    return d, true
}

// isValidCPF reports whether an 11-digit string is a valid Brazilian CPF
// (matching both check digits). Numbers with all identical digits are rejected
// by the algorithm naturally only for the check digits, so we guard them too.
func isValidCPF(d string) bool {
    if len(d) != 11 {
        return false
    }
    allEqual := true
    for i := 1; i < 11; i++ {
        if d[i] != d[0] {
            allEqual = false
            break
        }
    }
    if allEqual {
        return false
    }

    check := func(n int) int {
        sum := 0
        weight := n + 1
        for i := 0; i < n; i++ {
            sum += int(d[i]-'0') * (weight - i)
        }
        r := (sum * 10) % 11
        if r == 10 {
            r = 0
        }
        return r
    }

    return check(9) == int(d[9]-'0') && check(10) == int(d[10]-'0')
}

// validUSNational validates a 10-digit NANP number: area code and exchange must
// both start with 2-9 (N), and the area code's middle digit is unrestricted.
func validUSNational(d string) bool {
    if len(d) != 10 {
        return false
    }
    area := d[0]
    exch := d[3]
    if area < '2' || area > '9' {
        return false
    }
    if exch < '2' || exch > '9' {
        return false
    }
    return true
}
