package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
)

// The flag and env names are the operator-facing contract, so the tests spell them out.
const (
	budgetFlagName = "argon2-memory-budget"
	budgetEnvName  = "PADMARK_ARGON2_MEMORY_BUDGET"
	budgetTooSmall = "must be at least --argon2-memory"
)

// parseBudgetFlag runs the real flag set and returns the parsed --argon2-memory-budget.
func parseBudgetFlag(t *testing.T, args ...string) int {
	t.Helper()

	var got int

	cmd := clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Flags = appFlags()
		command.Action = func(_ context.Context, cmd *cli.Command) error {
			got = cmd.Int(budgetFlagName)

			return nil
		}
	})

	require.NoError(t, cmd.Run(t.Context(), append([]string{"padmark"}, args...)))

	return got
}

func TestArgon2MemoryBudgetFlagDefaultsTo128MiB(t *testing.T) {
	require.Equal(t, 128*1024, parseBudgetFlag(t))
}

func TestArgon2MemoryBudgetFlagParsesFromEnv(t *testing.T) {
	t.Setenv(budgetEnvName, "65536")

	require.Equal(t, 65536, parseBudgetFlag(t))
}

func (s *CommandSuite) TestServerRejectsArgon2MemoryBudgetBelowArgon2Memory() {
	tests := []struct {
		name string
		env  string
		args []string
	}{
		{
			name: "flag", args: []string{"--argon2-memory", "65536", "--" + budgetFlagName, "32768"},
			env: "",
		},
		{
			name: "env below default memory", env: "1024",
			args: nil,
		},
	}

	for _, testCase := range tests {
		s.Run(testCase.name, func() {
			if testCase.env != "" {
				s.T().Setenv(budgetEnvName, testCase.env)
			}

			dir := s.T().TempDir()
			dsn := filepath.Join(dir, "padmark.db")
			args := append([]string{"padmark", "serve", "--dsn", dsn}, testCase.args...)

			err := NewApp().Run(s.T().Context(), args)

			s.Require().ErrorContains(err, budgetTooSmall)
			s.Require().NotContains(err.Error(), "keyring", "configuration error must precede keyring startup")

			entries, readErr := os.ReadDir(dir)
			s.Require().NoError(readErr)
			s.Require().Empty(entries, "no database (or journal) file may be created")
		})
	}
}

func TestArgon2MemoryUsageDoesNotClaimEditCodesAreUnaffected(t *testing.T) {
	var usage string

	for _, flag := range appFlags() {
		intFlag, ok := flag.(*cli.IntFlag)
		if ok && intFlag.Name == "argon2-memory" {
			usage = intFlag.Usage
		}
	}

	require.NotEmpty(t, usage, "--argon2-memory flag must exist")
	require.NotContains(t, usage, "unaffected", "caller-chosen edit codes are hashed with argon2id")
	require.NotContains(t, usage, "fast hash and are")
	require.Contains(t, usage, "edit code")
	require.Contains(t, usage, budgetFlagName, "usage must point operators at the concurrency budget")
}
