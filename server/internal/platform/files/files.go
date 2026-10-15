package files

import (
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	Root string
}

func (s Store) Put(key string, data []byte) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (s Store) Get(key string) ([]byte, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s Store) Delete(key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s Store) path(key string) (string, error) {
	if key == "" || strings.Contains(key, "..") || filepath.IsAbs(key) {
		return "", os.ErrInvalid
	}
	clean := filepath.Clean(key)
	full := filepath.Join(s.Root, clean)
	rel, err := filepath.Rel(s.Root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", os.ErrInvalid
	}
	return full, nil
}
