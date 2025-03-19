//go:build windows

package ascii

func GetNextSpinner(label string) string { 
	switch label {
	    case "[=====]":
	        return "[ ====]"
	    case  "[ ====]":
	        return "[  ===]"
	    case  "[  ===]":
	        return "[=  ==]"
	    case "[=  ==]":
	        return "[==  =]"
	    case  "[==  =]":
	        return "[===  ]"
	    case "[===  ]":
	        return "[==== ]"
	    default:
	        return "[=====]"
	}
}