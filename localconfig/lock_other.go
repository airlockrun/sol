//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package localconfig

import (
	"context"
	"errors"
	"os"
)

func lockFile(context.Context, *os.File) error {
	return errors.New("sol config: file locking unsupported on this platform")
}
func unlockFile(*os.File) {}
func syncDirectory(string) error {
	return errors.New("sol config: directory sync unsupported on this platform")
}
func privatePath(string, os.FileMode) error {
	return errors.New("sol config: private storage unsupported on this platform")
}
func checkPrivateFile(string, os.FileInfo) error {
	return errors.New("sol config: private storage unsupported on this platform")
}
func replaceFile(string, string) error {
	return errors.New("sol config: private storage unsupported on this platform")
}
