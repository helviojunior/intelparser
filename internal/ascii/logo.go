package ascii

// Logo returns the intelparser ascii logo
func Logo() string {
	return `                   
                                                     
  _____       _       _ _____                         
 |_   _|     | |     | |  __ \                        
   | |  _ __ | |_ ___| | |__) |_ _ _ __ ___  ___ _ __ 
   | | | '_ \| __/ _ \ |  ___/ _' | '__/ __|/ _ \ '__|
  _| |_| | | | ||  __/ | |  | (_| | |  \__ \  __/ |   
 |_____|_| |_|\__\___|_|_|   \__,_|_|  |___/\___|_|   
                                                      
                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   
`
}

// LogoHelp returns the logo, with help
func LogoHelp(s string) string {
	return Logo() + "\n\n" + s
}
