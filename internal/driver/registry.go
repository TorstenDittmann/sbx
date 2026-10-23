package driver

import (
	"regexp"
	"strings"
)

// RegistryAuth is a credential for one image registry. Server is a host as
// RegistryHost returns it. Set Username and Password, or IdentityToken.
type RegistryAuth struct {
	Server        string
	Username      string
	Password      string
	IdentityToken string
}

// DockerHub is the host RegistryHost returns for unqualified references.
const DockerHub = "docker.io"

// RegistryHost returns the registry an image reference is pulled from, in
// lower case. "alpine", "library/alpine", and "index.docker.io/x" are all
// docker.io.
func RegistryHost(ref string) string {
	first, _, ok := strings.Cut(ref, "/")
	if !ok || !strings.ContainsAny(first, ".:") && first != "localhost" {
		return DockerHub
	}
	return NormalizeRegistry(first)
}

// NormalizeRegistry turns a configured server ("https://ghcr.io",
// "index.docker.io/v1/") into the form RegistryHost returns.
func NormalizeRegistry(server string) string {
	s := strings.ToLower(strings.TrimSpace(server))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s, _, _ = strings.Cut(s, "/")
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return DockerHub
	}
	return s
}

// Repository returns ref without its tag or digest, with the registry host
// spelled out, so "alpine:3.20" and "docker.io/library/alpine@sha256:..."
// name the same repository.
func Repository(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	host := RegistryHost(name)
	path := name
	if first, rest, ok := strings.Cut(name, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		path = rest
	}
	if host == DockerHub && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return host + "/" + path
}

var imageIDRE = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

// IsImageID reports whether ref names a local image by ID rather than by
// repository. Such a reference is never pulled.
func IsImageID(ref string) bool {
	return imageIDRE.MatchString(ref)
}
