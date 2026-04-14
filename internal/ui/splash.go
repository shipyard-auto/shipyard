package ui

import "strings"

func HelpSplash() string {
	lines := []string{
		Paint("                               |    |    |                              ", StyleBlue, StyleBold),
		Paint("                              )_)  )_)  )_)                             ", StyleBlue, StyleBold),
		Paint("                             )___))___))___)\\                           ", StyleBlue, StyleBold),
		Paint("                            )____)____)_____)\\\\                         ", StyleBlue, StyleBold),
		Paint("                          _____|____|____|____\\\\__                      ", StyleBlue, StyleBold),
		Paint("                 ---------\\                       /---------             ", StyleBlue, StyleBold),
		Paint("                   ^^^^^ ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^               ", StyleCyan),
		Paint("                          SHIPYARD :: TERMINAL DRYDOCK                 ", StyleWhite, StyleBold),
		Paint("                    Build, install and service your fleet              ", StyleDim),
	}

	return strings.Join(lines, "\n")
}
