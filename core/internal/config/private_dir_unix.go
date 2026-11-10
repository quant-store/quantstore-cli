//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config directory is not a private directory: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("config directory is not owned by the current user: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config directory permissions are not private: %s", path)
	}
	return nil
}
