//go:build integration

package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/tests/internal/servertest"
)

// postNoteStatus creates a note with the given number of content characters and returns the
// response status.
func postNoteStatus(t *testing.T, baseURL string, characters int) int {
	t.Helper()

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: nil, CheckRedirect: nil, Jar: nil,
	}
	defer client.CloseIdleConnections()

	body := `{"content":"` + strings.Repeat("a", characters) + `"}`
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/notes", strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	return response.StatusCode
}

// TestNoteContentLimit starts the real server and checks that the note content limit comes from
// its settings: --max-content-length, or --max-body-bytes when that flag is not set.
func (s *ServerSuite) TestNoteContentLimit() {
	t, binary := s.T(), s.binary

	t.Run("default follows the body limit", func(t *testing.T) {
		baseURL, _ := servertest.Start(t, binary, t.TempDir())

		// One character more than the fixed limit of earlier versions.
		require.Equal(t, http.StatusCreated, postNoteStatus(t, baseURL, 1<<20+1))
		require.Equal(t, http.StatusRequestEntityTooLarge, postNoteStatus(t, baseURL, 4<<20))
	})

	t.Run("body limit from the environment raises the content limit", func(t *testing.T) {
		baseURL, _ := servertest.StartWithEnv(t, binary, t.TempDir(), []string{"PADMARK_MAX_BODY_BYTES=8388608"})

		require.Equal(t, http.StatusCreated, postNoteStatus(t, baseURL, 5<<20))
	})

	t.Run("content limit flag", func(t *testing.T) {
		baseURL, _ := servertest.Start(t, binary, t.TempDir(), "serve", "--max-content-length", "100")

		require.Equal(t, http.StatusCreated, postNoteStatus(t, baseURL, 100))
		require.Equal(t, http.StatusUnprocessableEntity, postNoteStatus(t, baseURL, 101))
	})

	// The encryption layer holds at most 64 MiB of content. A body limit above that must not let
	// a longer note reach the encryption step, where it was an internal error (500).
	t.Run("content over the storage byte limit", func(t *testing.T) {
		baseURL, _ := servertest.Start(t, binary, t.TempDir(), "serve", "--max-body-bytes", "83886080")

		require.Equal(t, http.StatusUnprocessableEntity, postNoteStatus(t, baseURL, 64<<20+1))
	})
}
