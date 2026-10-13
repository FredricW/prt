package daemon

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LockFile is the name of a repo's pipeline lock in <gitDir>/prt.
const LockFile = "pipeline.lock"

// LockOwner describes the process that runs a repo's pipeline: written into
// the lock file by LockRepo, so a process that cannot take the lock can say
// who holds it and attach to its socket.
type LockOwner struct {
	PID int `json:"pid"`
	// What is how the owner was started: "prt daemon", "prt daemon --root
	// <dir>", "prt (no daemon)", "prt --root <dir> (no daemon)".
	What string `json:"what"`
	// Socket is where the owner serves the repo ("" for an in-process TUI or
	// snapshot); Root is the workspace root of a workspace owner.
	Socket string `json:"socket,omitempty"`
	Root   string `json:"root,omitempty"`
}

// String is "pid 123 (prt daemon --root /home/u/work)", or "another prt
// process" when the lock file could not be read.
func (o LockOwner) String() string {
	if o.PID <= 0 {
		return "another prt process"
	}
	return fmt.Sprintf("pid %d (%s)", o.PID, cmp.Or(o.What, "prt"))
}

// BusyError is LockRepo's error when another process runs the repo's
// pipeline.
type BusyError struct {
	Owner LockOwner
}

func (e *BusyError) Error() string {
	return "this repository is already analysed by " + e.Owner.String()
}

// LockRepo takes the exclusive pipeline lock of the repo whose git dir is
// gitDir (<gitDir>/prt/pipeline.lock, an advisory flock released when the
// process exits) and records owner in it (owner.PID is filled in). Only
// one process may run a repo's pipeline: a second one would reset the
// first one's in-flight items at startup (recovery assumes the previous
// process is dead), turn its pending comments back into drafts and pay for
// the same analyses twice. Take it before NewInProcess*; release it after
// the client is closed. When another process holds it the error is a
// *BusyError naming that process.
func LockRepo(gitDir string, owner LockOwner) (release func(), err error) {
	dir := filepath.Join(gitDir, "prt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, LockFile)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // our own .git/prt
	if err != nil {
		return nil, err
	}
	locked, err := tryLock(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon: lock %s: %w", p, err)
	}
	if !locked {
		_ = f.Close()
		return nil, &BusyError{Owner: readOwner(p)}
	}
	owner.PID = os.Getpid()
	if b, err := json.Marshal(owner); err == nil {
		_ = f.Truncate(0)
		_, _ = f.WriteAt(append(b, '\n'), 0)
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// readOwner reads the lock file's owner (zero when it cannot be read).
func readOwner(p string) LockOwner {
	var o LockOwner
	if b, err := os.ReadFile(p); err == nil { //nolint:gosec // our own .git/prt
		_ = json.Unmarshal(b, &o)
	}
	return o
}
