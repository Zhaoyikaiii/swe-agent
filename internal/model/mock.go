package model

import (
	"context"
	"errors"
	"sync"

	"github.com/local/swe-agent/internal/core"
)

type Mock struct {
	mu        sync.Mutex
	responses []string
	index     int
}

func NewMock(responses []string) *Mock {
	if len(responses) == 0 {
		responses = []string{"```swe_shell\nsubmit\n```"}
	}
	return &Mock{responses: responses}
}

func (m *Mock) Complete(ctx context.Context, req core.ModelRequest) (core.ModelResponse, error) {
	select {
	case <-ctx.Done():
		return core.ModelResponse{}, ctx.Err()
	default:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index >= len(m.responses) {
		return core.ModelResponse{}, errors.New("mock model has no more responses")
	}
	content := m.responses[m.index]
	m.index++
	return core.ModelResponse{
		Message: core.Message{Role: core.RoleAssistant, Content: content},
		Usage: core.Usage{
			InputTokens:  len(req.Messages) * 16,
			OutputTokens: len(content) / 4,
		},
		FinishReason: "stop",
	}, nil
}
