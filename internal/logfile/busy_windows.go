package logfile

import (
	"errors"
	"syscall"
)

// busy reports whether Windows refused the rename because another program
// has the file open (access denied or sharing violation).
func busy(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	const errorAccessDenied, errorSharingViolation, errorLockViolation = 5, 32, 33
	return errno == errorAccessDenied || errno == errorSharingViolation || errno == errorLockViolation
}
