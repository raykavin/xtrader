//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package remote

import "os"

// lock opens file, created when missing: without flock, another process of
// the agent isn't kept out.
func lock(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
}

// unlock closes f.
func unlock(f *os.File) error {
	return f.Close()
}
