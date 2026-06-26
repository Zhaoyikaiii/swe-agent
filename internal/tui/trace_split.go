package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func splitPanelWidths(width int, gap int, leftRatio int, leftMin int, rightMin int) (int, int) {
	leftWidth := max(leftMin, width*leftRatio/100)
	rightWidth := max(rightMin, width-leftWidth-gap)
	if leftWidth+gap+rightWidth > width {
		leftWidth = max(rightMin, width-gap-rightWidth)
	}
	return leftWidth, rightWidth
}

func splitPanelHeight(left string, right string, height int, heightOffset int) int {
	panelHeight := max(lipgloss.Height(left), lipgloss.Height(right))
	if height > 0 {
		panelHeight = min(panelHeight, max(8, height-heightOffset))
	}
	return panelHeight
}

func renderSplitPanels(left string, right string, leftWidth int, rightWidth int) string {
	divider := mutedStyle.Width(1).Render("|")
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Width(leftWidth).Render(left),
		" ",
		divider,
		" ",
		lipgloss.NewStyle().Width(rightWidth).Render(right),
	)
}

func fitListAroundCursor(content string, height int, cursor int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) <= height {
		return fitHeight(content, height)
	}

	headerLines := min(3, len(lines))
	bodyHeight := height - headerLines
	if bodyHeight <= 0 {
		return fitHeight(content, height)
	}

	body := lines[headerLines:]
	if len(body) <= bodyHeight {
		return fitHeight(content, height)
	}

	bodyCursor := clamp(cursor, 0, len(body)-1)
	start := clamp(bodyCursor-bodyHeight/2, 0, max(0, len(body)-bodyHeight))
	out := append([]string{}, lines[:headerLines]...)
	out = append(out, body[start:min(len(body), start+bodyHeight)]...)
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}
