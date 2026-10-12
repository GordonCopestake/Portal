package main

import "fmt"

const (
	colourGrey   = "\x1b[38;2;128;128;128m"
	colourOrange = "\x1b[38;2;255;165;0m"
	colourWhite  = "\x1b[38;2;255;255;255m"
	colourReset  = "\x1b[0m"
)

func (a *agent) write(colour, text string) {
	// Escape untrusted controls before adding our own colour sequences.
	text = terminalText(text)
	if a.colour {
		fmt.Fprint(a.out, colour, text, colourReset)
	} else {
		fmt.Fprint(a.out, text)
	}
}
