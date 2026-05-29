package rules

import (
    "testing"

    "github.com/helviojunior/intelparser/pkg/models"
)

// extractPhones mimics the relevant part of the runner's detectRule loop: it
// finds every candidate with the rule regex and runs the PostProcessor,
// returning the formatted numbers that survived validation.
func extractPhones(t *testing.T, text string) []models.Phone {
    t.Helper()
    r := Phone()
    var out []models.Phone
    for _, m := range r.Regex.FindAllString(text, -1) {
        f := &models.Finding{Secret: m}
        ok, err := r.PostProcessor(f)
        if err != nil {
            t.Fatalf("postprocessor error for %q: %v", m, err)
        }
        if ok && f.Phone.Phone != "" {
            out = append(out, f.Phone)
        }
    }
    return out
}

func TestPhoneValid(t *testing.T) {
    cases := []struct {
        in      string
        country string
        phone   string
    }{
        // Brazilian mobile, all formats
        {"+55 11 91234-5678", "BR", "5511912345678"},
        {"+55 (11) 91234-5678", "BR", "5511912345678"},
        {"(11) 91234-5678", "BR", "5511912345678"},
        {"11 91234-5678", "BR", "5511912345678"},
        {"11912345678", "BR", "5511912345678"},
        {"5511912345678", "BR", "5511912345678"},
        {"+5511912345678", "BR", "5511912345678"},
        // Brazilian landline
        {"(41) 3555-1234", "BR", "554135551234"},
        {"+55 41 3555-1234", "BR", "554135551234"},
        {"41 3555-1234", "BR", "554135551234"},
        // US, all formats
        {"+1 (415) 555-2671", "US", "14155552671"},
        {"(415) 555-2671", "US", "14155552671"},
        {"415-555-2671", "US", "14155552671"},
        {"415.555.2671", "US", "14155552671"},
        {"415 555 2671", "US", "14155552671"},
        {"+1 415 555 2671", "US", "14155552671"},
        {"+14155552671", "US", "14155552671"},
    }
    // Note: a glued bare 10-digit number like "4155552671" is intentionally
    // treated as BR (DDD 41 landline) in this Brazil-centric tool, since without
    // a +1 or 3-digit area grouping it cannot be told apart from a BR landline.

    for _, c := range cases {
        got := extractPhones(t, "ligue para "+c.in+" agora")
        if len(got) == 0 {
            t.Errorf("%q: expected a match, got none", c.in)
            continue
        }
        found := false
        for _, p := range got {
            if p.Phone == c.phone && p.Country == c.country {
                found = true
            }
        }
        if !found {
            t.Errorf("%q: expected %s/%s, got %+v", c.in, c.country, c.phone, got)
        }
    }
}

func TestPhoneInvalid(t *testing.T) {
    negatives := []string{
        "12345678",               // too short
        "4111 1111 1111 1111",    // credit card (16 digits)
        "0000000000",             // not a valid plan
        "order id 20231234567",   // DDD 20 invalid, not US
        "1234567890123456789",    // 19-digit run
        "valor 1.234.567,89",     // money
        "ip 192.168.0.1",         // ip-ish
        "12567356800",            // glued 11-digit CPF, must NOT become a US phone
        "CPF;12337447804;NOME",   // CPF in a labelled field
    }
    for _, n := range negatives {
        got := extractPhones(t, n)
        if len(got) != 0 {
            t.Errorf("%q: expected no match, got %+v", n, got)
        }
    }
}
