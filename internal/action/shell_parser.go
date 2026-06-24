package action

import (
	"errors"
	"regexp"
	"strings"

	"github.com/local/swe-agent/internal/core"
)

type Parser struct{}

var fencedBlock = regexp.MustCompile("(?s)```(?:swe_shell|bash|sh|shell)?\\s*\\n(.*?)\\n```")

func NewParser() Parser {
	return Parser{}
}

func (Parser) Parse(resp core.ModelResponse) ([]core.ToolCall, error) {
	if len(resp.ToolCalls) > 0 {
		return resp.ToolCalls, nil
	}
	content := strings.TrimSpace(resp.Message.Content)
	if content == "" {
		return nil, errors.New("empty model response")
	}
	matches := fencedBlock.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		return nil, errors.New("expected exactly one action block")
	}
	cmd := strings.TrimSpace(matches[0][1])
	if cmd == "" {
		return nil, errors.New("empty shell action")
	}
	if cmd == "submit" {
		return []core.ToolCall{{Name: "submit"}}, nil
	}
	return []core.ToolCall{{
		Name: "shell",
		Args: map[string]any{"command": cmd},
	}}, nil
}
