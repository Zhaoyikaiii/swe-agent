package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func stringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	v, ok := args[name]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func intArg(args map[string]any, name string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch v := args[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var out int
		if _, err := fmt.Sscanf(v, "%d", &out); err == nil {
			return out
		}
	}
	return fallback
}

func safePath(root, p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(p) {
		return "", errors.New("absolute paths are not allowed")
	}
	clean := filepath.Clean(p)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", errors.New("path escapes workspace")
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return full, nil
}

func isSkippableDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "__pycache__", "dist", "build", "target":
		return true
	default:
		return false
	}
}

func readTextFile(path string, maxBytes int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data[:maxBytes]), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
