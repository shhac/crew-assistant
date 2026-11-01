package work

import "os"

// On Windows the home is beside the state, never under a shared directory.
func ownedByUs(os.FileInfo) bool { return true }
