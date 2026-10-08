//go:build !unix

package remote

// syncDir does nothing where a directory can't be synced, eg: Windows.
func syncDir(string) error {
	return nil
}
