// Package arch_test checks the import rules of the Onion layers. The layer table of the global Go
// standard is adapted to this project: repositories live in internal/infra/storage, and the
// composition root of the server lives in internal/infra/server (see CLAUDE.md).
package arch_test

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// module is the import path of the application module.
const module = "github.com/partyzanex/padmark"

// layer is a group of packages with the same import rules.
type layer string

const (
	layerDomain   layer = "domain"
	layerUsecases layer = "usecases"
	layerAdapters layer = "adapters"
	layerInfra    layer = "infra"
	layerShared   layer = "shared"
	layerClient   layer = "client"
	layerCmd      layer = "cmd"
	layerTests    layer = "tests"
	layerData     layer = "data"
)

// layerRule maps a package tree (relative to the module) to its layer: the package at prefix and
// all packages below it.
type layerRule struct {
	prefix string
	layer  layer
}

// layerRules lists every package group of the module. A new package outside these groups fails
// TestLayerImports, so its author must give it a layer here.
func layerRules() []layerRule {
	return []layerRule{
		{prefix: "internal/domain", layer: layerDomain},
		{prefix: "internal/usecases/", layer: layerUsecases},
		{prefix: "internal/adapters/", layer: layerAdapters},
		{prefix: "internal/infra/", layer: layerInfra},
		// Small packages without business rules that more than one layer uses: error context,
		// CLI flag helpers, build information, and the client rules of padmark-cli and padmark-mcp.
		{prefix: "internal/errorcontext", layer: layerShared},
		{prefix: "internal/clifactory", layer: layerShared},
		{prefix: "internal/buildinfo", layer: layerShared},
		{prefix: "internal/clientauth", layer: layerShared},
		// The public Go client of the API. It must not depend on the internal packages.
		{prefix: "pkg/client", layer: layerClient},
		{prefix: "cmd/", layer: layerCmd},
		// Test helpers and integration suites may import each layer to build a test stack.
		{prefix: "internal/testutil", layer: layerTests},
		{prefix: "tests/", layer: layerTests},
		// Embedded SQL migrations: data only.
		{prefix: "migrations/", layer: layerData},
	}
}

// allowedImports gives, for each layer, the layers that its packages may import. A layer that
// is not in the list of its own layer may still import its own packages when sameTree permits
// it. The arrows point inward: domain ← usecases ← adapters and infra ← cmd.
func allowedImports() map[layer][]layer {
	return map[layer][]layer{
		layerDomain:   {},
		layerUsecases: {layerDomain, layerShared},
		// An adapter must not import infra or another adapter: adapters talk through usecase
		// interfaces. Its own subpackages (for example web/middleware) are permitted by sameTree.
		layerAdapters: {layerDomain, layerUsecases, layerShared, layerClient},
		// The server composition root in infra builds the adapters.
		layerInfra:  {layerDomain, layerUsecases, layerAdapters, layerInfra, layerShared, layerData},
		layerShared: {layerDomain, layerShared, layerClient},
		layerClient: {},
		layerCmd:    {layerDomain, layerUsecases, layerAdapters, layerInfra, layerShared, layerClient, layerData},
		layerTests: {
			layerDomain, layerUsecases, layerAdapters, layerInfra, layerShared, layerClient, layerData, layerTests,
		},
		layerData: {},
	}
}

// knownExceptions lists imports that break the rules today, as "importer -> imported" with the
// paths relative to the module. Each entry needs a reason. Remove an entry when its import goes
// away: TestLayerImports fails for an entry that no longer matches an import.
func knownExceptions() map[string]string {
	return map[string]string{
		// runAction of padmark-mcp is the composition root of its binary: it builds the stderr
		// logger with the level parser of the server. Moving the composition to cmd/padmark-mcp
		// removes this import.
		"internal/adapters/mcp -> internal/infra/logging": "composition root of padmark-mcp in the adapter",
	}
}

// listedPackage is the part of `go list -json` output that the test reads. The tags repeat the
// field names of go list.
type listedPackage struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

func TestLayerImports(t *testing.T) {
	t.Parallel()

	packages := listPackages(t)
	require.NotEmpty(t, packages)

	allowed := allowedImports()
	exceptions := knownExceptions()
	usedExceptions := make(map[string]bool, len(exceptions))

	for _, pkg := range packages {
		from, ok := relative(pkg.ImportPath)
		if !ok {
			continue
		}

		fromLayer, known := layerOf(from)
		if !known {
			t.Errorf("package %s has no layer; add it to layerRules", from)

			continue
		}

		for _, imported := range pkg.Imports {
			target, inModule := relative(imported)
			if !inModule || sameTree(from, target) {
				continue
			}

			toLayer, toKnown := layerOf(target)
			if toKnown && slices.Contains(allowed[fromLayer], toLayer) {
				continue
			}

			edge := from + " -> " + target
			if _, excepted := exceptions[edge]; excepted {
				usedExceptions[edge] = true

				continue
			}

			t.Errorf("layer rule broken: %s (%s may not import %s)", edge, fromLayer, toLayer)
		}
	}

	for edge := range exceptions {
		if !usedExceptions[edge] {
			t.Errorf("known exception %q matches no import; remove it from knownExceptions", edge)
		}
	}
}

func TestLayerRules_ClassifyKnownPackages(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]layer{
		"internal/domain":                  layerDomain,
		"internal/usecases/notes":          layerUsecases,
		"internal/adapters/web/middleware": layerAdapters,
		"internal/infra/server":            layerInfra,
		"internal/clientauth":              layerShared,
		"pkg/client":                       layerClient,
	} {
		got, ok := layerOf(path)
		require.True(t, ok, path)
		require.Equal(t, want, got, path)
	}

	_, ok := layerOf("internal/unknown")
	require.False(t, ok, "a package outside the rules has no layer")
}

// listPackages runs `go list -json` for all packages of the module. The test runs in
// tests/arch, which is inside the module, so the pattern resolves from there.
func listPackages(t *testing.T) []listedPackage {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "go", "list", "-json", module+"/...")

	out, err := cmd.Output()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		require.NoError(t, err, "go list: %s", exitErr.Stderr)
	}

	require.NoError(t, err)

	var packages []listedPackage

	dec := json.NewDecoder(strings.NewReader(string(out)))

	for {
		var pkg listedPackage

		decodeErr := dec.Decode(&pkg)
		if errors.Is(decodeErr, io.EOF) {
			return packages
		}

		require.NoError(t, decodeErr)

		packages = append(packages, pkg)
	}
}

// relative returns the path of a module package relative to the module root.
func relative(importPath string) (string, bool) {
	if importPath == module {
		return "", true
	}

	return strings.CutPrefix(importPath, module+"/")
}

// layerOf returns the layer of a package path relative to the module.
func layerOf(path string) (layer, bool) {
	for _, rule := range layerRules() {
		root := strings.TrimSuffix(rule.prefix, "/")
		if path == root || strings.HasPrefix(path, root+"/") {
			return rule.layer, true
		}
	}

	return "", false
}

// sameTree reports whether two packages belong to one component: one adapter, one usecase or
// one infra package with its subpackages. For example, internal/adapters/web may import
// internal/adapters/web/middleware, but internal/adapters/cli may not import
// internal/adapters/mcp.
func sameTree(from, to string) bool {
	return componentOf(from) != "" && componentOf(from) == componentOf(to)
}

// componentOf returns the component root of a package: the first path segment below
// internal/adapters, internal/usecases or internal/infra. Other packages have no component.
func componentOf(path string) string {
	for _, base := range []string{"internal/adapters/", "internal/usecases/", "internal/infra/"} {
		rest, ok := strings.CutPrefix(path, base)
		if !ok {
			continue
		}

		name, _, _ := strings.Cut(rest, "/")

		return base + name
	}

	return ""
}
