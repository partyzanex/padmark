package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGet_ServiceUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want string
	}{
		{"disabled", `{"message":"translated text","code":"api_disabled"}`, "API is disabled on this server (api_disabled)"},
		{"other failure", `{"message":"maintenance"}`, "service unavailable: maintenance"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)

				_, err := w.Write([]byte(testCase.body))
				if err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()

			err := runCLI(t.Context(), "--url", server.URL, "get", "example")
			require.ErrorContains(t, err, testCase.want)
		})
	}
}
