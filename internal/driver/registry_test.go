package driver

import "testing"

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"alpine":                   "docker.io",
		"alpine:3.20":              "docker.io",
		"library/alpine":           "docker.io",
		"acme/app:1":               "docker.io",
		"ghcr.io/acme/app:1":       "ghcr.io",
		"GHCR.io/acme/app":         "ghcr.io",
		"localhost/app":            "localhost",
		"localhost:5000/app":       "localhost:5000",
		"index.docker.io/acme/app": "docker.io",
		"registry.example.com:443/a/b@sha256:abc": "registry.example.com:443",
	}
	for ref, want := range cases {
		if got := RegistryHost(ref); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestNormalizeRegistry(t *testing.T) {
	cases := map[string]string{
		"https://index.docker.io/v1/": "docker.io",
		"docker.io":                   "docker.io",
		"https://GHCR.io":             "ghcr.io",
		"registry.example.com:5000/":  "registry.example.com:5000",
	}
	for in, want := range cases {
		if got := NormalizeRegistry(in); got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepository(t *testing.T) {
	cases := map[string]string{
		"alpine":                             "docker.io/library/alpine",
		"alpine:3.20":                        "docker.io/library/alpine",
		"docker.io/library/alpine@sha256:ab": "docker.io/library/alpine",
		"acme/app:1":                         "docker.io/acme/app",
		"ghcr.io/acme/app:1":                 "ghcr.io/acme/app",
		"localhost:5000/app:2":               "localhost:5000/app",
		"localhost:5000/app":                 "localhost:5000/app",
	}
	for ref, want := range cases {
		if got := Repository(ref); got != want {
			t.Errorf("Repository(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestIsImageID(t *testing.T) {
	if !IsImageID("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") || !IsImageID("0123456789ab") {
		t.Fatal("ids not recognized")
	}
	if IsImageID("alpine") || IsImageID("alpine:3.20") {
		t.Fatal("names taken for ids")
	}
}
