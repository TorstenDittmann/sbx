package agent

import "errors"

type unsupportedError string

func (e unsupportedError) Error() string { return string(e) }

func errUnsupported(msg string) error { return unsupportedError(msg) }

// IsUnsupported reports platform limitations such as PTY on non-Linux.
func IsUnsupported(err error) bool {
	var u unsupportedError
	return errors.As(err, &u)
}
