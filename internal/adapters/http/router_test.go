package http

import (
	net_http "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// spyResponseWriter counts how many times WriteHeader is called by the code under test.
type spyResponseWriter struct {
	net_http.ResponseWriter

	writeHeaderCalls int
	lastStatus       int
}

func (s *spyResponseWriter) Header() net_http.Header     { return net_http.Header{} }
func (s *spyResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (s *spyResponseWriter) WriteHeader(status int) {
	s.writeHeaderCalls++
	s.lastStatus = status
}

// TestStatusRecorder_WriteHeader_DelegatesOnce verifies that calling WriteHeader twice
// on statusRecorder only delegates to the underlying ResponseWriter once.
// The second call must be a no-op: the underlying writer must not receive it, because
// net/http logs a "superfluous response.WriteHeader call" warning when it happens.
func TestStatusRecorder_WriteHeader_DelegatesOnce(t *testing.T) {
	spy := &spyResponseWriter{}
	sre := &statusRecorder{ResponseWriter: spy}

	sre.WriteHeader(net_http.StatusOK)
	sre.WriteHeader(net_http.StatusInternalServerError) // must be ignored

	assert.Equal(t, 1, spy.writeHeaderCalls,
		"underlying ResponseWriter.WriteHeader must be called exactly once")
	assert.Equal(t, net_http.StatusOK, sre.status,
		"sr.status must reflect the first call")
	assert.Equal(t, net_http.StatusOK, spy.lastStatus,
		"underlying writer must have received only the first status")
}

// errorWriter always fails on Write — used to simulate a broken connection.
type errorWriter struct {
	spyResponseWriter

	body []byte
}

func (e *errorWriter) Write(b []byte) (int, error) {
	e.body = append(e.body, b...)
	return 0, assert.AnError
}

// TestAPISpec_WriteError_DoesNotCorruptBody verifies that when w.Write fails,
// APISpec does not call http.Error and therefore does not append error text to the body.
func TestAPISpec_WriteError_DoesNotCorruptBody(t *testing.T) {
	wer := &errorWriter{}
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(wer, req)

	// http.Error writes "failed to write spec\n" — assert it is absent.
	assert.NotContains(t, string(wer.body), "failed to write spec",
		"http.Error must not be called after a partial Write")
	// WriteHeader must not have been called — no status change attempted.
	assert.Equal(t, 0, wer.writeHeaderCalls,
		"WriteHeader must not be called when Write fails")
}

// TestAPISpec_OK_SetsContentType verifies the happy path: correct Content-Type header.
func TestAPISpec_OK_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := net_http.NewRequest(net_http.MethodGet, "/api/openapi.yaml", nil)

	APISpec(rec, req)

	assert.Equal(t, net_http.StatusOK, rec.Code)
	assert.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Body.Bytes())
}
