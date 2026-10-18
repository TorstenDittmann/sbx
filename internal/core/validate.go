package core

import (
	"path"
	"regexp"
	"strings"
)

var (
	envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	labelRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func cleanPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || strings.Contains(p, `\`) {
		return "", invalid("path must be an absolute posix path")
	}
	if !strings.HasPrefix(p, "/") {
		return "", invalid("path must be absolute")
	}
	c := path.Clean(p)
	if c == "." || !strings.HasPrefix(c, "/") {
		return "", invalid("path must be absolute")
	}
	return c, nil
}

func validEnv(env map[string]string) error {
	if len(env) > 64 {
		return invalid("too many environment variables")
	}
	for k, v := range env {
		if !envKeyRE.MatchString(k) {
			return fmtInvalid("invalid env name %q", k)
		}
		if strings.ContainsRune(v, 0) || len(v) > 32*1024 {
			return fmtInvalid("invalid env value for %s", k)
		}
	}
	return nil
}

func validLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return invalid("too many labels")
	}
	for k, v := range labels {
		if !labelRE.MatchString(k) {
			return fmtInvalid("invalid label %q", k)
		}
		if strings.ContainsAny(v, "\r\n") || len(v) > 256 {
			return fmtInvalid("invalid label value for %s", k)
		}
	}
	return nil
}

func validImage(ref string) error {
	if ref == "" || len(ref) > 512 || strings.ContainsAny(ref, " \t\r\n") {
		return invalid("image is required")
	}
	return nil
}

func validTemplateName(name string) error {
	if !nameRE.MatchString(name) {
		return invalid("template name must match " + nameRE.String())
	}
	return nil
}

func validCmd(cmd []string) error {
	if len(cmd) == 0 || len(cmd) > 64 {
		return invalid("cmd must contain 1 to 64 arguments")
	}
	for _, c := range cmd {
		if c == "" || strings.ContainsRune(c, 0) || len(c) > 4096 {
			return invalid("cmd arguments must be non-empty and free of NUL")
		}
	}
	return nil
}
