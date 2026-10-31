package docs_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/buildinfo"
)

func readDocument(t *testing.T, path string, handler http.HandlerFunc) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	return recorder.Body.String()
}

func TestIndexFileLists(t *testing.T) {
	discardLog := slog.New(slog.DiscardHandler)

	index := readDocument(t, "/llms.txt", adhttp.LLMsTxt(discardLog))
	require.True(t, strings.HasPrefix(index, "# "))

	linkPattern := regexp.MustCompile(`^- \[[^\]]+\]\(([^)]+)\)(: .*)?$`)
	base, err := url.Parse("https://padmark.example:8443/llms.txt")
	require.NoError(t, err)

	inFileList, links := false, 0

	for line := range strings.SplitSeq(index, "\n") {
		if strings.HasPrefix(line, "## ") {
			inFileList = true

			continue
		}

		if !inFileList || line == "" {
			continue
		}

		match := linkPattern.FindStringSubmatch(line)
		require.Len(t, match, 3, "H2 sections must contain resource links: %s", line)
		resource, parseErr := url.Parse(match[1])
		require.NoError(t, parseErr)

		resolved := base.ResolveReference(resource)
		require.Equal(t, base.Scheme, resolved.Scheme)
		require.Equal(t, base.Host, resolved.Host, "links must stay on the target instance")

		links++
	}

	require.Positive(t, links)
}

func TestAgentDocumentsMatchOpenAPIRelease(t *testing.T) {
	discardLog := slog.New(slog.DiscardHandler)

	spec := readDocument(t, "/api/openapi.yaml", adhttp.APISpec(discardLog))
	versionPattern := regexp.MustCompile(`(?m)^  version: "?([^"\s]+)"?$`)
	version := versionPattern.FindStringSubmatch(spec)
	require.Len(t, version, 2)
	require.Equal(t, buildinfo.Version(), version[1])

	for path, handler := range map[string]http.HandlerFunc{
		"/llms.txt": adhttp.LLMsTxt(discardLog), "/api/reference.md": adhttp.APIReference(discardLog),
	} {
		document := readDocument(t, path, handler)
		require.Contains(t, document, "release "+version[1], path)
		require.NotContains(t, document, "{{VERSION}}", path)
	}
}
