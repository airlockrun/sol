//go:build !unix

package tools

import "os"

func openReadDescriptor(path string) (*os.File, error) {
	return os.Open(path)
}
