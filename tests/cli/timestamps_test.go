package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	adaptercli "github.com/partyzanex/padmark/internal/adapters/cli"
)

func TestCLITimestampsDoNotDependOnSystemTimezone(t *testing.T) {
	isolateCLI(t)
	server := timestampServer(t)
	t.Setenv(adaptercli.EnvURL, server.URL)
	t.Setenv(adaptercli.EnvToken, "timestamp-test-token")

	for _, zone := range []string{"America/Sao_Paulo", "Asia/Tokyo"} {
		t.Run(zone, func(t *testing.T) {
			t.Setenv("TZ", zone)

			for _, test := range []struct {
				name       string
				timestamps map[string]string
			}{
				{
					name: "get",
					timestamps: map[string]string{
						"created": "2026-10-24T16:02:03Z", "updated": "2026-10-25T07:05:06Z",
						"expires": "2026-10-26T03:58:59Z",
					},
				},
				{
					name: "expand",
					timestamps: map[string]string{
						"created": "2026-10-24T16:02:03Z", "expires": "2026-10-26T03:58:59Z",
					},
				},
				{
					name:       "create",
					timestamps: map[string]string{"expires": "2026-10-26T03:58:59Z"},
				},
				{
					name:       "shorten",
					timestamps: map[string]string{"expires": "2026-10-26T03:58:59Z"},
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					command := timestampCommand(t, test.name)
					output, err := command.CombinedOutput()
					require.NoError(t, err, string(output))

					fields := make(map[string]string)

					for line := range strings.SplitSeq(string(output), "\n") {
						if key, value, ok := strings.Cut(line, ":"); ok {
							fields[key] = strings.TrimSpace(value)
						}
					}

					for field, expected := range test.timestamps {
						require.Equal(t, expected, fields[field], "%s in %s", field, zone)
					}
				})
			}
		})
	}
}

func timestampCommand(t *testing.T, name string) *exec.Cmd {
	t.Helper()

	switch name {
	case "get":
		return exec.CommandContext(t.Context(), "go", "run", "../../cmd/padmark-cli", "get", "example")
	case "expand":
		return exec.CommandContext(t.Context(), "go", "run", "../../cmd/padmark-cli", "expand", "example")
	case "create":
		return exec.CommandContext(t.Context(), "go", "run", "../../cmd/padmark-cli", "create", "--content", "body")
	case "shorten":
		return exec.CommandContext(t.Context(), "go", "run", "../../cmd/padmark-cli",
			"shorten", "--target-url", "https://example.com")
	default:
		t.Fatalf("unknown CLI case %q", name)

		return nil
	}
}

func timestampServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"id": "example", "views": 0,
			"created_at": "2026-10-25T01:02:03+09:00",
			"expires_at": "2026-10-25T23:58:59-04:00",
		}
		if strings.HasPrefix(r.URL.Path, "/links") {
			body["target_url"], body["burn_after_click"] = "https://example.com", false
		} else {
			body["title"], body["content"], body["content_type"] = "title", "body", "text/markdown"
			body["updated_at"], body["burn_after_reading"] = "2026-10-25T04:05:06-03:00", false
		}

		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			body["edit_code"] = "timestamp-test-code"

			w.WriteHeader(http.StatusCreated)
		}

		err := json.NewEncoder(w).Encode(body)
		if err != nil {
			t.Errorf("write timestamp response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}
