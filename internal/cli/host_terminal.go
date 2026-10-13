package cli

import (
	"io"
	"os"
)

func isInputTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && isTerminal(file)
}

func isOutputTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && isTerminal(file)
}
