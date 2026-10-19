package atomicfile

import (
	"errors"
	"syscall"
)

// isUnsupported reports an fsync a file system or OS does not support on
// directories.
func isUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, errors.ErrUnsupported)
}
