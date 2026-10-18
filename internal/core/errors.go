package core

import "fmt"

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
