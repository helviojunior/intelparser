//go:build !windows

package ascii

func GetNextSpinner(spin string) string { 
	chars := []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", //"⠿",
	}

	if spin == "" || spin == "⠿" {
		return chars[0]
	}

	for idx, e := range chars {
		if spin == e {
			if idx + 1 >= len(chars) {
				return chars[0]
			}else {
				return chars[idx + 1]
			}
		}
	}

	return "⠿"
}