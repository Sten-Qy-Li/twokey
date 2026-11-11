// Package lock makes sure only one twokey process works on a repository
// (SPEC sections 7 and 13). The lock is a file in the run folder that
// holds the PID and the run ID.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileName is the lock file's name inside run_dir.
const FileName = "lock"

// Info is what the lock file holds.
type Info struct {
	PID     int       `json:"pid"`
	RunID   string    `json:"run_id"`
	Started time.Time `json:"started"`
}

// Lock is a held lock.
type Lock struct{ path string }

// HeldError means another lock file exists.
type HeldError struct {
	Path  string
	Info  Info
	Alive bool // the PID in the lock is still running
}

func (e *HeldError) Error() string {
	if e.Alive {
		return fmt.Sprintf("twokey (PID %d) is running run %s in this repository", e.Info.PID, e.Info.RunID)
	}
	return fmt.Sprintf("a stale lock from run %s exists (PID %d is not running): %s", e.Info.RunID, e.Info.PID, e.Path)
}

// Acquire creates the lock file in runDir. It fails with a *HeldError if
// a lock file already exists.
func Acquire(runDir string, info Info) (*Lock, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(runDir, FileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		held, rerr := Read(runDir)
		if rerr != nil {
			return nil, fmt.Errorf("a lock file exists but can't be read: %s: %w", path, rerr)
		}
		return nil, &HeldError{Path: path, Info: held, Alive: Alive(held.PID)}
	}
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(info)
	_, werr := f.Write(append(data, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return nil, werr
	}
	return &Lock{path: path}, nil
}

// Release removes the lock file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	err := os.Remove(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Read returns the lock file's content. It returns fs.ErrNotExist if
// there is no lock.
func Read(runDir string) (Info, error) {
	var info Info
	data, err := os.ReadFile(filepath.Join(runDir, FileName))
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(data, &info)
	return info, err
}

// Remove deletes a stale lock. It refuses if the PID in it is running.
func Remove(runDir string) (Info, error) {
	info, err := Read(runDir)
	if err != nil {
		return info, err
	}
	if Alive(info.PID) {
		return info, &HeldError{Path: filepath.Join(runDir, FileName), Info: info, Alive: true}
	}
	return info, os.Remove(filepath.Join(runDir, FileName))
}
