package trajectory

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/local/swe-agent/internal/core"
)

type JSONLStore struct {
	path string
	mu   sync.Mutex
	file *os.File
}

func NewJSONLStore(dir string) (*JSONLStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &JSONLStore{path: path, file: file}, nil
}

func (s *JSONLStore) Append(ctx context.Context, event core.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return s.file.Sync()
}

func (s *JSONLStore) Load(ctx context.Context) ([]core.Event, error) {
	return LoadFile(ctx, s.path)
}

func LoadFile(ctx context.Context, path string) ([]core.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []core.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		var event core.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func (s *JSONLStore) Path() string {
	return s.path
}

func (s *JSONLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
