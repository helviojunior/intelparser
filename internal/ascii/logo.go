package ascii

import (
	"fmt"
	"strings"
	"github.com/helviojunior/intelparser/internal/version"
)

// Logo returns the intelparser ascii logo
func Logo() string {
	txt := `                   
                                                     
  _____       _       _ _____                         
 |_   _|     | |     | |  __ \                        
   | |  _ __ | |_ ___| | |__) |_ _ _ __ ___  ___ _ __ 
   | | | '_ \| __/ _ \ |  ___/ _' | '__/ __|/ _ \ '__|
  _| |_| | | | ||  __/ | |  | (_| | |  \__ \  __/ |   
 |_____|_| |_|\__\___|_|_|   \__,_|_|  |___/\___|_|   
`
	v := fmt.Sprintf("Version: %s.%s", version.Version, version.GitHash)
	txt += strings.Repeat(" ", 51 - len(v))
	txt += v
	return fmt.Sprintln(txt)
}

// LogoHelp returns the logo, with help
func LogoHelp(s string) string {
	return fmt.Sprintln(Logo()) + s
}
