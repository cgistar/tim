package core

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
)

type Active struct {
	Runtime string
}
type Process struct {
	PID     int
	Start   string
	Binary  string
	Runtime string
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

// 启动前仅文件缺失触发路径选择；权限或配置错误仍交给原错误处理。
func (s *Store) KernelMissing() (bool, error) {
	settings, err := s.Settings()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(s.ResolvePath(settings.MihomoPath))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
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
	if err = s.removeObsoleteState(); err != nil {
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
	// 启动配置固定位置，不随数据目录切换。
	if len(parts) == 1 && parts[0] == "setting.yaml" {
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
func (s *Store) Settings() (Settings, error) {
	b, err := os.ReadFile(s.Path("setting.yaml"))
	if err != nil {
		return Settings{}, err
	}
	return ParseSettings(b)
}
func (s *Store) Active() (Active, error) {
	path := s.Path("config.yaml")
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Active{}, nil
	}
	if err != nil {
		return Active{}, err
	}
	return Active{Runtime: path}, nil
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

// 旧状态不再参与身份判断；清理仅限当前数据目录的已废弃文件。
func (s *Store) removeObsoleteState() error {
	err := os.Remove(s.Path("state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
