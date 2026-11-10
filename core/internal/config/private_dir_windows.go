//go:build windows

package config

import (
	"fmt"
	"os"
)

// UserConfigDir resolves to the per-user AppData tree on Windows. Native CI
// must additionally verify the inherited ACL; Go's portable FileMode cannot.
func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config directory is not a private directory: %s", path)
	}
	return nil
}
