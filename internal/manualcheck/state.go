package manualcheck

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// ListNames returns the sandbox names of a `list` output, skipping the header.
func ListNames(list string) []string {
	var names []string
	for index, line := range strings.Split(strings.ReplaceAll(list, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if index == 0 || len(fields) == 0 {
			continue
		}
		names = append(names, fields[0])
	}
	return names
}

// ListState returns the state columns of one sandbox from a `list` output, or an empty string.
func ListState(list, name string) string {
	for index, line := range strings.Split(strings.ReplaceAll(list, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if index == 0 || len(fields) == 0 || fields[0] != name {
			continue
		}
		return strings.Join(fields[1:], " ")
	}
	return ""
}

// combined runs a command and returns its output whatever the exit status.
func combined(command *exec.Cmd) string {
	output, _ := command.CombinedOutput()
	return string(output)
}

// sortedLines sorts the non-empty lines of text.
func sortedLines(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool { return line == "" })
	slices.Sort(lines)
	return strings.Join(lines, "\n") + "\n"
}

// CaptureState writes the state an item must see unchanged: `list`, the containers, the volumes,
// and the inspect output of each named sandbox and its backup container.
func (r *Run) CaptureState(path string, sandboxes ...string) error {
	var buffer bytes.Buffer
	buffer.WriteString(combined(r.SA("list")))
	buffer.WriteString(sortedLines(combined(r.Podman("ps", "--all", "--format", "{{.Names}} {{.ID}} {{.Image}} {{.State}}"))))
	buffer.WriteString(sortedLines(combined(r.Podman("volume", "ls", "--format", "{{.Name}}"))))
	for _, name := range sandboxes {
		buffer.WriteString(combined(r.Podman("inspect", r.Container(name))))
		buffer.WriteString(combined(r.Podman("inspect", r.BackupContainer(name))))
	}
	return writePrivate(path, buffer.String())
}

// CompareFiles returns nil when the two files are byte-for-byte equal and an error naming the
// first differing line otherwise.
func CompareFiles(before, after string) error {
	left, err := os.ReadFile(before)
	if err != nil {
		return err
	}
	right, err := os.ReadFile(after)
	if err != nil {
		return err
	}
	if bytes.Equal(left, right) {
		return nil
	}
	leftLines, rightLines := strings.Split(string(left), "\n"), strings.Split(string(right), "\n")
	for index := range max(len(leftLines), len(rightLines)) {
		var l, rr string
		if index < len(leftLines) {
			l = leftLines[index]
		}
		if index < len(rightLines) {
			rr = rightLines[index]
		}
		if l != rr {
			return fmt.Errorf("%s and %s differ at line %d: %q vs %q", before, after, index+1, l, rr)
		}
	}
	return fmt.Errorf("%s and %s differ", before, after)
}

var imageLine = regexp.MustCompile(`^([0-9a-f]{64}) `)

// ImageInventory writes the full image IDs of the whole engine with their references, and every
// container with the image ID it references, sorted.
func (r *Run) ImageInventory(path string) error {
	var buffer bytes.Buffer
	buffer.WriteString("# images: id repository:tag\n")
	images, err := r.PodmanOutput("images", "--all", "--no-trunc", "--format", "{{.ID}} {{.Repository}}:{{.Tag}}")
	if err != nil {
		return err
	}
	buffer.WriteString(sortedLines(strings.ReplaceAll(images, "sha256:", "")))
	buffer.WriteString("# containers: name image-id\n")
	containers, err := r.PodmanOutput("ps", "--all", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	var rows []string
	for _, name := range strings.Fields(containers) {
		image, err := r.PodmanOutput("container", "inspect", "--format", "{{.Image}}", name)
		if err != nil {
			image = "unknown"
		}
		rows = append(rows, name+" "+image)
	}
	buffer.WriteString(sortedLines(strings.Join(rows, "\n")))
	return writePrivate(path, buffer.String())
}

// ImageIDs returns the image IDs of an inventory file.
func ImageIDs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "# containers") {
			break
		}
		if match := imageLine.FindStringSubmatch(line); match != nil {
			ids = append(ids, match[1])
		}
	}
	return ids, nil
}

// CompareImages returns the IDs of the first inventory that the second lacks.
func CompareImages(before, after string) ([]string, error) {
	earlier, err := ImageIDs(before)
	if err != nil {
		return nil, err
	}
	later, err := ImageIDs(after)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range earlier {
		if !slices.Contains(later, id) {
			missing = append(missing, id)
		}
	}
	return missing, nil
}
