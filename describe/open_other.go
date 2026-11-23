//go:build !unix

package describe

import "os"

const openFlags = os.O_RDONLY

// childPath is the path the child process reads the file by: its real
// path, checked just before.
func childPath(real string) string { return real }
