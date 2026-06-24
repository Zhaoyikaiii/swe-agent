package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/local/swe-agent/internal/core"
)

type OpenAICompatible struct {
	Client  *http.Client
	BaseURL string
	APIKey  string
	Model   string
}

func NewOpenAICompatible(baseURL, apiKeyEnv, modelName string) (*OpenAICompatible, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENAI_API_KEY"
	}
	key := os.Getenv(apiKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("%s is not set", apiKeyEnv)
	}
	return &OpenAICompatible{
		Client:  &http.Client{Timeout: 120 * time.Second},
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  key,
		Model:   modelName,
	}, nil
}

func (m *OpenAICompatible) Complete(ctx context.Context, req core.ModelRequest) (core.ModelResponse, error) {
	payload := chatRequest{
		Model:       m.Model,
		Messages:    make([]chatMessage, 0, len(req.Messages)),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	for _, msg := range req.Messages {
		payload.Messages = append(payload.Messages, chatMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
			Name:    msg.Name,
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.ModelResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return core.ModelResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.APIKey)

	resp, err := m.Client.Do(httpReq)
	if err != nil {
		return core.ModelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody bytes.Buffer
		_, _ = errBody.ReadFrom(resp.Body)
		return core.ModelResponse{}, fmt.Errorf("chat completion failed: status=%d body=%s", resp.StatusCode, errBody.String())
	}
	var decoded chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return core.ModelResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return core.ModelResponse{}, fmt.Errorf("chat completion returned no choices")
	}
	choice := decoded.Choices[0]
	return core.ModelResponse{
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: choice.Message.Content,
		},
		Usage: core.Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
		},
		FinishReason: choice.FinishReason,
	}, nil
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
