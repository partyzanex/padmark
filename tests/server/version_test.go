package server_test

import (
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/buildinfo"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

func TestAPIDocumentationReleaseVersion(t *testing.T) {
	for _, test := range []struct {
		name, version string
		flags         []string
	}{
		{name: "source build", version: buildinfo.Version(), flags: nil},
		{
			name: "release build", version: "0.4.1-rc.1",
			flags: []string{"-ldflags=-X github.com/partyzanex/padmark/internal/buildinfo.version=v0.4.1-rc.1"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := servertest.Build(t, test.flags...)
			//nolint:gosec // freshly built test binary and a fixed read-only argument
			output, err := exec.CommandContext(t.Context(), binary, "--version").CombinedOutput()
			require.NoError(t, err, string(output))
			require.Equal(t, "padmark version "+test.version, strings.TrimSpace(string(output)))

			baseURL, _ := servertest.Start(t, binary, t.TempDir())

			client := &http.Client{Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()

			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/openapi.yaml", nil)
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)

			defer response.Body.Close()

			require.Equal(t, http.StatusOK, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)

			match := regexp.MustCompile(`(?m)^ {2}version: "([^"]+)"$`).FindSubmatch(body)
			require.Len(t, match, 2)
			require.Equal(t, test.version, string(match[1]))
			require.Contains(t, string(body), "title: Padmark API")
		})
	}
}
