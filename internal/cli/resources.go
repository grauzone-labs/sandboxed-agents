package cli

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

var digits = regexp.MustCompile(`^[0-9]+$`)
var decimal = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,3})?$`)

// Resources holds up's resource limits; sizes are in bytes.
type Resources struct {
	Memory    int64
	CPUs      float64
	PIDsLimit int64
	ShmSize   int64
}

func DefaultResources() Resources {
	return Resources{Memory: 8 * 1024 * 1024 * 1024, CPUs: 4, PIDsLimit: 2048, ShmSize: 1024 * 1024 * 1024}
}

func positiveInteger(value string) (int64, error) {
	if !digits.MatchString(value) {
		return 0, fmt.Errorf("expected a positive whole number")
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("value must be between 1 and %d", int64(math.MaxInt64))
	}
	return number, nil
}

func size(value string) (int64, error) {
	multiplier := int64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'k', 'K':
			multiplier = 1 << 10
		case 'm', 'M':
			multiplier = 1 << 20
		case 'g', 'G':
			multiplier = 1 << 30
		case 't', 'T':
			multiplier = 1 << 40
		}
		if multiplier != 1 {
			value = value[:len(value)-1]
		}
	}
	number, err := positiveInteger(value)
	if err != nil {
		return 0, err
	}
	if number > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("size exceeds %d bytes", int64(math.MaxInt64))
	}
	return number * multiplier, nil
}

func (r *Resources) parse(options map[string]string) error {
	for _, option := range []string{"memory", "cpus", "pids-limit", "shm-size", "port"} {
		value, exists := options[option]
		if !exists {
			continue
		}
		var err error
		switch option {
		case "memory":
			r.Memory, err = size(value)
			if err == nil && r.Memory < 6*1024*1024 {
				err = fmt.Errorf("minimum memory is 6m")
			}
		case "shm-size":
			r.ShmSize, err = size(value)
		case "pids-limit":
			r.PIDsLimit, err = positiveInteger(value)
		case "port":
			var port int64
			port, err = positiveInteger(value)
			if err == nil && port > 65535 {
				err = fmt.Errorf("port exceeds 65535")
			}
		case "cpus":
			if !decimal.MatchString(value) {
				err = fmt.Errorf("expected a positive number with at most three decimals")
			} else {
				r.CPUs, err = strconv.ParseFloat(value, 64)
				if err == nil && r.CPUs <= 0 {
					err = fmt.Errorf("cpus must be greater than zero")
				}
				if err == nil && r.CPUs > 9223372036.854 {
					err = fmt.Errorf("cpus must be at most 9223372036.854")
				}
			}
		}
		if err != nil {
			return fmt.Errorf("--%s: %w", option, err)
		}
	}
	return nil
}
