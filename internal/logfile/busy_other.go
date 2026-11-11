//go:build !windows

package logfile

import (
	"errors"
	"io/fs"
)

// busy reports whether the rename failed because the file is locked. On
// Unix a rename doesn't fail for an open file; only permission errors are
// retried.
func busy(err error) bool { return errors.Is(err, fs.ErrPermission) }
