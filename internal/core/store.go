package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
)

type Active struct {
	Runtime string `json:"runtime"`
}
type Process struct {
	PID     int    `json:"pid"`
	Start   string `json:"start"`
	Binary  string `json:"binary"`
	Runtime string `json:"runtime"`
}
type Store struct {
	ConfigDir    string
	DataOverride string
	dataDir      atomic.Value
}

func (s *Store) DataDir() string { return s.dataDir.Load().(string) }

func (s *Store) effectiveDataDir(settings Settings) string {
	if s.DataOverride != "" {
		return s.ResolvePath(s.DataOverride)
	}
	return s.ResolvePath(settings.DataDir)
}

// 内核选择只读取管理设置，相对路径以 setting.yaml 所在目录为基准。
func (s *Store) Kernel() (string, error) {
	settings, err := s.Settings()
	if err != nil {
		return "", err
	}
	return resolveBinary(s.ResolvePath(settings.MihomoPath))
}

func (s *Store) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(s.ConfigDir, path)
}

func Open(dir string) (*Store, error) {
	p, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(p, 0700); err != nil {
		return nil, err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	s := &Store{ConfigDir: p, DataOverride: os.Getenv("DATA")}
	if runtime.GOOS == "linux" {
		unlock, err := lockFile(s.Path("operation.lock"))
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	if err = s.initializeSettings(); err != nil {
		return nil, err
	}
	settings, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if err = s.setInitialDataDir(s.effectiveDataDir(settings)); err != nil {
		return nil, err
	}
	if err = s.initializeState(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) setInitialDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for _, sub := range []string{"home", "kernels"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return err
		}
	}
	s.dataDir.Store(dir)
	return nil
}

func (s *Store) Path(parts ...string) string {
	// 启动配置和操作锁固定位置，切换数据目录时仍使用同一配置与锁。
	if len(parts) == 1 && (parts[0] == "setting.yaml" || parts[0] == "operation.lock") {
		return filepath.Join(s.ConfigDir, parts[0])
	}
	return filepath.Join(append([]string{s.DataDir()}, parts...)...)
}
func AtomicWrite(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, b, 0600)
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("读取 %s: %w", filepath.Base(path), err)
	}
	return nil
}
func (s *Store) Settings() (Settings, error) {
	b, err := os.ReadFile(s.Path("setting.yaml"))
	if err != nil {
		return Settings{}, err
	}
	return ParseSettings(b)
}
func (s *Store) Active() (Active, error) {
	state, err := s.State()
	v := Active{}
	if state.Applied {
		v.Runtime = s.Path("config.yaml")
	}
	return v, err
}
func (s *Store) User() (*Document, error) {
	b, err := os.ReadFile(s.Path("user.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return NewUser()
	}
	if err != nil {
		return nil, err
	}
	return ParseUser(b)
}
func (s *Store) Base() (*Document, error) {
	b, err := os.ReadFile(s.Path("subscription.yaml"))
	if err != nil {
		return nil, fmt.Errorf("请先下载有效订阅: %w", err)
	}
	return Parse(b)
}
func (s *Store) Preview() (*Document, []string, error) {
	base, err := s.Base()
	if err != nil {
		return nil, nil, err
	}
	u, err := s.User()
	if err != nil {
		return nil, nil, err
	}
	settings, err := s.Settings()
	if err != nil {
		return nil, nil, err
	}
	return BuildConfigured(base, u, settings)
}
