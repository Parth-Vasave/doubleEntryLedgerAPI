// Package api embeds the OpenAPI specification served by the HTTP API.
package api

import _ "embed"

// OpenAPI is the OpenAPI 3.1 specification of the ledger API.
//
//go:embed openapi.yaml
var OpenAPI []byte
