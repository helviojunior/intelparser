package rules

import (
    "testing"

    "github.com/helviojunior/intelparser/pkg/models"
)

func extractDocs(t *testing.T, text string) []models.Document {
    t.Helper()
    r := Document()
    var out []models.Document
    for _, m := range r.Regex.FindAllString(text, -1) {
        f := &models.Finding{Secret: m}
        ok, err := r.PostProcessor(f)
        if err != nil {
            t.Fatalf("postprocessor error for %q: %v", m, err)
        }
        if ok && f.Document.Number != "" {
            out = append(out, f.Document)
        }
    }
    return out
}

func TestDocumentValid(t *testing.T) {
    cases := []struct {
        in     string
        number string
        cpf    bool
        cnpj   bool
    }{
        // CPF (valid check digits), formatted and glued
        {"529.982.247-25", "52998224725", true, false},
        {"52998224725", "52998224725", true, false},
        {"111.444.777-35", "11144477735", true, false},
        // CNPJ (valid check digits), formatted and glued
        {"11.222.333/0001-81", "11222333000181", false, true},
        {"11222333000181", "11222333000181", false, true},
    }

    for _, c := range cases {
        got := extractDocs(t, "doc "+c.in+" fim")
        found := false
        for _, d := range got {
            if d.Number == c.number && d.IsCPF == c.cpf && d.IsCNPJ == c.cnpj {
                found = true
            }
        }
        if !found {
            t.Errorf("%q: expected number=%s cpf=%v cnpj=%v, got %+v", c.in, c.number, c.cpf, c.cnpj, got)
        }
    }
}

func TestDocumentInvalid(t *testing.T) {
    negatives := []string{
        "12345678901",            // 11 digits, wrong CPF check digits
        "12345678000199",         // 14 digits, wrong CNPJ check digits
        "00000000000",            // repeated digits
        "00000000000000",         // repeated digits
        "123.456.789-00",         // formatted but invalid CPF
        "telefone 11987654321",   // a phone, not a CPF
    }
    for _, n := range negatives {
        got := extractDocs(t, n)
        if len(got) != 0 {
            t.Errorf("%q: expected no match, got %+v", n, got)
        }
    }
}
