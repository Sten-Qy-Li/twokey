package logfile

import "syscall"

var busyErr error = syscall.Errno(32) // ERROR_SHARING_VIOLATION
