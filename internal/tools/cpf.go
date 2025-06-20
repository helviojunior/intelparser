package tools

import (
	"regexp"
	"strconv"
	"strings"
	re "regexp"
)

var cpfRe = re.MustCompile(`\b(\d{3}[.\-]?\d{3}[.\-]?\d{3}[.\-]?\d{2})\b`)
var cleanCpf = regexp.MustCompile(`[^0-9]`)

func ExtractCPF(text string) (bool, string) {
	groups := cpfRe.FindStringSubmatch(text)
    if len(groups) == 2 {
    	if validateCPF(groups[1]) {
    		return true, cleanCpf.ReplaceAllString(groups[1], "")
    	}
    }

    return false, ""
}

func validateCPF(cpf string) bool {
	// Remove dots and dashes
	cpf = cleanCpf.ReplaceAllString(cpf, "")

	// Must be 11 digits
	if len(cpf) != 11 {
		return false
	}

	// Reject all digits equal (e.g., "11111111111")
	for i := 0; i < 10; i++ {
		if cpf == strings.Repeat(strconv.Itoa(i), 11) {
			return false
		}
	}

	// Validate first digit
	sum := 0
	for i := 0; i < 9; i++ {
		num, _ := strconv.Atoi(string(cpf[i]))
		sum += num * (10 - i)
	}
	d1 := (sum * 10) % 11
	if d1 == 10 {
		d1 = 0
	}
	if d1 != int(cpf[9]-'0') {
		return false
	}

	// Validate second digit
	sum = 0
	for i := 0; i < 10; i++ {
		num, _ := strconv.Atoi(string(cpf[i]))
		sum += num * (11 - i)
	}
	d2 := (sum * 10) % 11
	if d2 == 10 {
		d2 = 0
	}
	if d2 != int(cpf[10]-'0') {
		return false
	}

	return true
}