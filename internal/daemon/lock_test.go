package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// One process at a time runs a repo's pipeline: a second LockRepo (another
// open file description, as another process would have) is refused with
// the owner's description, and the lock is free again after release.
func TestLockRepo(t *testing.T) {
	gitDir := t.TempDir()
	owner := LockOwner{What: "prt daemon --root /w", Socket: "/s/prtd.sock", Root: "/w"}
	release, err := LockRepo(gitDir, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LockRepo(gitDir, LockOwner{What: "prt (no daemon)"})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second lock: %v, want a BusyError", err)
	}
	want := owner
	want.PID = os.Getpid()
	if busy.Owner != want {
		t.Errorf("owner = %+v, want %+v", busy.Owner, want)
	}
	if got := busy.Error(); got != "this repository is already analysed by pid "+strconv.Itoa(want.PID)+" (prt daemon --root /w)" {
		t.Errorf("error = %q", got)
	}
	release()
	again, err := LockRepo(gitDir, LockOwner{What: "prt (no daemon)"})
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
	if _, err := os.Stat(filepath.Join(gitDir, "prt", LockFile)); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestLockOwnerString(t *testing.T) {
	if got := (LockOwner{}).String(); got != "another prt process" {
		t.Errorf("unknown owner = %q", got)
	}
	if got := (LockOwner{PID: 7}).String(); got != "pid 7 (prt)" {
		t.Errorf("owner without what = %q", got)
	}
}
