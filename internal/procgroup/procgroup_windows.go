//go:build windows

package procgroup

import "os/exec"

// Detach leaves cmd as it is: a console's Ctrl-C is not the signal a Unix
// terminal sends to its whole group.
func Detach(cmd *exec.Cmd) {}
