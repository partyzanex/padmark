package cli

import "context"

// Shared test fixtures: the binary name and command names are defined once here so test files
// reference these constants instead of repeating string literals (which would trip goconst).
const (
	testBin   = "padmark-cli"
	cmdCreate = "create"
	cmdEdit   = "edit"
	cmdPing   = "ping"
	cmdGet    = "get"
)

// runCLI runs the root command with the binary name prepended, mirroring real argv.
func runCLI(ctx context.Context, args ...string) error {
	return NewApp().Run(ctx, append([]string{testBin}, args...))
}
