//go:build unix

package remote

import (
	"errors"
	"os"
)

// syncDir syncs the directory dir, so that a file renamed or created in it
// stays so after a crash of the machine.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
