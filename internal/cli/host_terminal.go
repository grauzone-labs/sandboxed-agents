package cli

import (
	"io"
	"os"
)

// isInputTerminal reports whether input is a file connected to a terminal.
func isInputTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && isTerminal(file)
}

// isOutputTerminal reports whether output is a file connected to a terminal.
func isOutputTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && isTerminal(file)
}
