package cli

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/shhac/crew-assistant/internal/server"
)

// maxSocketPath stays under the smallest Unix socket path limit, macOS's 104
// bytes with its terminator.
const maxSocketPath = 100

// listenOwner opens the owner's private socket: in the runtime directory, or,
// when that path is too long for a socket, in a fresh private directory under
// the system's temporary one. remove takes away what it made.
func listenOwner(runtimeDir string) (listener net.Listener, path string, remove func(), err error) {
	dir, remove := runtimeDir, func() {}
	if len(filepath.Join(dir, "owner.sock")) > maxSocketPath {
		if dir, err = os.MkdirTemp("", "c-"); err != nil {
			return nil, "", nil, err
		}
		remove = func() { _ = os.RemoveAll(dir) }
	}
	path = filepath.Join(dir, "owner.sock")
	if len(path) > maxSocketPath {
		remove()
		return nil, "", nil, errors.New("the temporary directory's path is too long for the daemon's private socket; set TMPDIR to a shorter one")
	}
	// A socket left by a daemon that stopped without closing it.
	if st, err := os.Lstat(path); err == nil && st.Mode().Type() == fs.ModeSocket {
		_ = os.Remove(path)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		remove()
		return nil, "", nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		remove()
		return nil, "", nil, err
	}
	return server.OwnerListener(l), path, remove, nil
}
