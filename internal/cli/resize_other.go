//go:build !unix

package cli

// onResize is a no-op where SIGWINCH does not exist.
func onResize(func()) (stop func()) { return func() {} }
