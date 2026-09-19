//go:build darwin || linux

package storage

import (
	"os"
	"syscall"
)

func secureDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return securePath(path, true)
}
func secureFile(path string) error { return securePath(path, false) }
func securePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSymlink != 0 {
		return ErrStorage
	}
	if directory {
		if !info.IsDir() {
			return ErrStorage
		}
	} else if !info.Mode().IsRegular() || stat.Nlink != 1 {
		return ErrStorage
	}
	if err := removeACL(path); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	return os.Chmod(path, mode)
}
