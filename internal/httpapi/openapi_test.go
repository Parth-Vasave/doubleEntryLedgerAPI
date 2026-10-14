package httpapi_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	apispec "github.com/Parth-Vasave/doubleEntryLedgerAPI/api"
	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/httpapi"
)

// TestOpenAPICoversAllRoutes keeps the spec and the router in sync: every
// registered /v1 route must be documented, and every documented operation
// must exist.
func TestOpenAPICoversAllRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(apispec.OpenAPI, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	s := httpapi.NewServer(httpapi.Deps{})
	s.Handler()
	registered := map[string]bool{}
	for _, r := range s.Routes() {
		if strings.Contains(r, " /v1/") || strings.HasSuffix(r, "z") {
			registered[r] = true
			if !documented[r] {
				t.Errorf("route %q is not documented in api/openapi.yaml", r)
			}
		}
	}
	if len(registered) < 20 {
		t.Fatalf("only %d routes registered; route discovery is broken", len(registered))
	}
	for op := range documented {
		if strings.Contains(op, " /v1/") && !registered[op] {
			t.Errorf("documented operation %q has no route", op)
		}
	}
}
