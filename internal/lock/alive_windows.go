package lock

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// Alive reports whether a process with this PID is running.
func Alive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists but belongs to someone else.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true // can't tell: assume it runs, so nothing is removed by mistake
	}
	const stillActive = 259
	return code == stillActive
}
