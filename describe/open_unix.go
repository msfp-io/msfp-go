//go:build unix

package describe

import (
	"os"
	"syscall"
)

// openFlags open a file read-only without following a last symbolic
// link, and without blocking on a FIFO or a device (refused once
// opened: not a regular file).
const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// childPath is the path the child process reads the open file by: its
// descriptor 3 (exec.Cmd's first ExtraFiles entry), so that ffprobe
// reads the file that was checked and no other, even if the path
// changed since.
func childPath(string) string { return "/dev/fd/3" }

// lower sets a child's niceness.
func lower(pid, nice int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice) }
