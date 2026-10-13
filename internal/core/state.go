package core

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

type State struct {
	Applied bool     `json:"applied"`
	Process *Process `json:"process,omitempty"`
}

func (s *Store) State() (State, error) {
	var state State
	err := readJSON(s.Path("state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	return state, err
}

func (s *Store) saveProcess(process *Process) error {
	state, err := s.State()
	if err != nil {
		return err
	}
	state.Process = process
	return writeJSON(s.Path("state.json"), state)
}

func (s *Store) initializeState() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if s.DataDir() != s.ConfigDir {
		unlock, err := lockFile(filepath.Join(s.DataDir(), "operation.lock"))
		if err != nil {
			return err
		}
		defer unlock()
	}
	state, err := s.State()
	if err != nil {
		return err
	}
	if state.Applied || state.Process != nil {
		raw, e := os.ReadFile(s.Path("config.yaml"))
		if e != nil {
			return e
		}
		if _, e = Parse(raw); e != nil {
			return e
		}
	}
	return nil
}
