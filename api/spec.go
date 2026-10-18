// Package apispec embeds the OpenAPI document.
package apispec

import _ "embed"

// OpenAPI is the v0.1 HTTP API.
//
//go:embed openapi.yaml
var OpenAPI []byte
