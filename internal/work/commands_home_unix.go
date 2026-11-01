//go:build !windows

package work

import (
	"os"
	"syscall"
)

func ownedByUs(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Getuid()
}
