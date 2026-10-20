package core

import (
	"fmt"
	"strconv"
)

// OpError is a client-visible failure with an HTTP status and stable code.
type OpError struct {
	Status int
	Code   string
	Msg    string
}

func (e *OpError) Error() string { return e.Msg }

func invalid(msg string) error {
	return &OpError{Status: 400, Code: "invalid", Msg: msg}
}

func notFound(msg string) error {
	return &OpError{Status: 404, Code: "not_found", Msg: msg}
}

func conflict(msg string) error {
	return &OpError{Status: 409, Code: "conflict", Msg: msg}
}

func tooLarge(msg string) error {
	return &OpError{Status: 413, Code: "payload_too_large", Msg: msg}
}

// FormatBytes renders a byte limit for error messages, e.g. "32 MiB".
func FormatBytes(n int) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return strconv.Itoa(n>>30) + " GiB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.Itoa(n>>10) + " KiB"
	}
	return strconv.Itoa(n) + " bytes"
}

func unsupported(msg string) error {
	return &OpError{Status: 400, Code: "unsupported", Msg: msg}
}

func quota(msg string) error {
	return &OpError{Status: 429, Code: "quota_exceeded", Msg: msg}
}

func forbidden(msg string) error {
	return &OpError{Status: 403, Code: "forbidden", Msg: msg}
}

func wrap(status int, code, msg string) error {
	return &OpError{Status: status, Code: code, Msg: msg}
}

func fmtInvalid(format string, args ...any) error {
	return invalid(fmt.Sprintf(format, args...))
}
