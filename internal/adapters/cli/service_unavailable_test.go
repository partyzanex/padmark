package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

type ServiceUnavailableSuite struct {
	suite.Suite
}

func TestServiceUnavailableSuite(t *testing.T) {
	suite.Run(t, new(ServiceUnavailableSuite))
}

func (s *ServiceUnavailableSuite) TestGet_ServiceUnavailable() {
	for _, testCase := range []struct {
		name string
		body string
		want string
	}{
		{"disabled", `{"message":"translated text","code":"api_disabled"}`, "API is disabled on this server (api_disabled)"},
		{"other failure", `{"message":"maintenance"}`, "service unavailable: maintenance"},
	} {
		s.Run(testCase.name, func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)

				_, err := w.Write([]byte(testCase.body))
				if err != nil {
					s.T().Error(err)
				}
			}))
			defer server.Close()

			err := runCLI(s.T().Context(), "--url", server.URL, "get", "example")
			s.Require().ErrorContains(err, testCase.want)
		})
	}
}
