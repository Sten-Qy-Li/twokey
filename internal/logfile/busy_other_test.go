//go:build !windows

package logfile

import "io/fs"

var busyErr error = fs.ErrPermission
