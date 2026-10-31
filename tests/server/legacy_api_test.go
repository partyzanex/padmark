package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/tests/internal/servertest"
)

func TestLegacyNoteHTTPContract(t *testing.T) {
	binary := servertest.Build(t)
	baseURL, _ := servertest.Start(t, binary, t.TempDir())
	client := &http.Client{Transport: nil, CheckRedirect: nil, Jar: nil, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	for _, test := range []struct {
		slug    string
		private bool
	}{
		{slug: "legacy-public", private: false},
		{slug: "legacy-private", private: true},
		{slug: "legacy/public", private: false},
		{slug: "legacy/private", private: true},
	} {
		t.Run(test.slug, func(t *testing.T) {
			const code = "legacy-edit-code"

			status, created := legacyJSONRequest(t, client, http.MethodPost, baseURL+"/notes", "", map[string]any{
				"content": "original", "slug": test.slug, "private": test.private, "edit_code": code,
			})
			require.Equal(t, http.StatusCreated, status)
			require.Equal(t, test.slug, created["id"])
			require.Equal(t, test.private, created["private"])
			require.Equal(t, code, created["edit_code"])

			noteURL := baseURL + "/notes/" + test.slug
			status, read := legacyJSONRequest(t, client, http.MethodGet, noteURL, "", nil)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, test.private, read["private"])
			require.Equal(t, "original", read["content"])

			status, updated := legacyJSONRequest(t, client, http.MethodPut, noteURL, "", map[string]any{
				"content": "updated", "private": test.private, "edit_code": code,
			})
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, test.private, updated["private"])
			require.Equal(t, "updated", updated["content"])

			deleteURL := noteURL + "?edit_code=" + code
			status, _ = legacyJSONRequest(t, client, http.MethodDelete, deleteURL, "wrong-header-code", nil)
			require.Equal(t, http.StatusForbidden, status, "the header must take precedence over a valid query code")

			status, _ = legacyJSONRequest(t, client, http.MethodDelete, deleteURL, "", nil)
			require.Equal(t, http.StatusNoContent, status, "legacy query-only deletion must still work")
			status, _ = legacyJSONRequest(t, client, http.MethodGet, noteURL, "", nil)
			require.Equal(t, http.StatusNotFound, status)
		})
	}
}

func legacyJSONRequest(
	t *testing.T, client *http.Client, method, target, editCode string, body map[string]any,
) (int, map[string]any) {
	t.Helper()

	var payload io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)

		payload = strings.NewReader(string(encoded))
	}

	request, err := http.NewRequestWithContext(t.Context(), method, target, payload)
	require.NoError(t, err)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	if editCode != "" {
		request.Header.Set("X-Edit-Code", editCode)
	}

	response, err := client.Do(request)
	require.NoError(t, err)

	defer response.Body.Close()

	var document map[string]any
	if response.StatusCode != http.StatusNoContent {
		require.NoError(t, json.NewDecoder(response.Body).Decode(&document))
	}

	return response.StatusCode, document
}
