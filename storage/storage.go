package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("state not found")

// StateStore defines persistence for session/state data (cookies, quotas, etc.).
// Concrete implementations can plug in file, Redis, or cloud stores later.
type StateStore interface {
	Save(ctx context.Context, key string, data []byte) error
	Load(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// NoopStore is a placeholder that satisfies StateStore without persisting data.
type NoopStore struct{}

func (NoopStore) Save(_ context.Context, _ string, _ []byte) error {
	return errors.New("not implemented")
}

func (NoopStore) Load(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (NoopStore) Delete(_ context.Context, _ string) error {
	return errors.New("not implemented")
}

// FileStore persists state as JSON blobs on disk under BaseDir, one file per key.
type FileStore struct {
	BaseDir string
}

func (f *FileStore) pathFor(key string) string {
	safe := filepath.Base(key)
	return filepath.Join(f.BaseDir, safe+".json")
}

func (f *FileStore) ensureDir() error {
	if f.BaseDir == "" {
		f.BaseDir = "data"
	}
	return os.MkdirAll(f.BaseDir, 0o755)
}

func (f *FileStore) Save(_ context.Context, key string, data []byte) error {
	if err := f.ensureDir(); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}
	if key == "" {
		return errors.New("empty key")
	}
	return os.WriteFile(f.pathFor(key), data, 0o600)
}

func (f *FileStore) Load(_ context.Context, key string) ([]byte, error) {
	if err := f.ensureDir(); err != nil {
		return nil, fmt.Errorf("ensure dir: %w", err)
	}
	if key == "" {
		return nil, errors.New("empty key")
	}
	b, err := os.ReadFile(f.pathFor(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

func (f *FileStore) Delete(_ context.Context, key string) error {
	if key == "" {
		return errors.New("empty key")
	}
	if err := os.Remove(f.pathFor(key)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}



