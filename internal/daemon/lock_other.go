//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package daemon

import "os"

// tryLock always succeeds where there is no flock: the platform runs
// without the pipeline lock (prt targets macOS and Linux).
func tryLock(*os.File) (bool, error) { return true, nil }
