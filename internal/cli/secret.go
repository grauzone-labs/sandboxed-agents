package cli

import (
	"fmt"
	"io"
	"os"
)

// readTerminalSecret disables echo before reading and restores terminal settings on every return path.
func readTerminalSecret(input io.Reader, prompt io.Writer) (token []byte, err error) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, fmt.Errorf("token input is not a terminal")
	}
	restore, err := hideTerminalInput(file)
	if err != nil {
		return nil, err
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil {
			token = nil
			err = restoreErr
		}
	}()
	if _, err := fmt.Fprint(prompt, "Token: "); err != nil {
		return nil, err
	}
	defer fmt.Fprintln(prompt)
	var result []byte
	var one [1]byte
	for {
		n, err := file.Read(one[:])
		if n > 0 {
			switch one[0] {
			case '\n', '\r':
				return result, nil
			case 3, 4:
				return nil, fmt.Errorf("token input cancelled")
			case 8, 127:
				if len(result) > 0 {
					result = result[:len(result)-1]
				}
			default:
				result = append(result, one[0])
			}
		}
		if err != nil {
			return nil, err
		}
	}
}
