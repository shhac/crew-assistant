package checktest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const ProgressEnv = "CREW_CHECK_PROGRESS"
const InvocationEnv = "CREW_CHECK_INVOCATION"
const maxProgressBytes = 16 << 20

// Progress belongs to one disposable check, never to a subsequent invocation.
// Done distinguishes a runner's terminal report from its last pre-SIGKILL save.
type Progress struct {
	Invocation string
	Stage      string
	Done       bool
	Report     Report
}

func SaveProgress(path string, p Progress) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > maxProgressBytes {
		return fmt.Errorf("check progress exceeds %d bytes", maxProgressBytes)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".check-progress-write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// OpenRoot prevents a workspace-supplied symlink from reading outside the copy.
// A killed writer leaves either the previous complete snapshot or an empty file.
func LoadProgress(dir, name, invocation string) (*Progress, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("check progress is not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProgressBytes+1))
	if err != nil || len(data) == 0 {
		return nil, err // Checks that do not use this runner retain their contract.
	}
	if len(data) > maxProgressBytes {
		return nil, fmt.Errorf("check progress exceeds %d bytes", maxProgressBytes)
	}
	var p Progress
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Invocation != invocation {
		return nil, fmt.Errorf("check progress belongs to another invocation")
	}
	if len(p.Report.Skips) > 2048 || len(p.Stage) > 2048 {
		return nil, fmt.Errorf("check progress exceeds evidence limits")
	}
	skipBytes := 0
	for i, s := range p.Report.Skips {
		skipBytes += SkipEvidenceSize(s)
		if len(s.Package)+len(s.Test) > 2048 || len(s.Reason) > 1024 || len(s.Exception) > 2048 {
			return nil, fmt.Errorf("check skip exceeds evidence limits")
		}
		// Workspace output cannot introduce exceptions that are not checked in.
		if s.Exception != "" && s.Exception != allowed(s.Package, s.Test, s.Reason, runtime.GOOS) {
			p.Report.Skips[i].Exception = ""
		}
		if p.Report.Skips[i].Exception == "" {
			p.Report.Failed = true
		}
	}
	if skipBytes > MaxSkipEvidenceBytes {
		return nil, fmt.Errorf("check progress exceeds skip evidence retention budget")
	}
	if p.Report.EvidenceLimited {
		p.Report.Failed, p.Report.Complete = true, false
	}
	return &p, nil
}
