package validation

import "strings"

func SummarizeOutput(output string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 80
	}
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	head := maxLines / 2
	tail := maxLines - head
	return strings.Join(lines[:head], "\n") + "\n<lines truncated>\n" + strings.Join(lines[len(lines)-tail:], "\n")
}
