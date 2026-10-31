package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

func (s *HandlerSuite) TestUpdateLink_OptionalFields() {
	// The anonymous mutation gate applies only with the account system.
	s.router = s.newRouterWithTOTPAuth()
	for _, testCase := range []struct {
		name, fields string
		clear        bool
	}{
		{"omitted", "", false},
		{"reset", `,"ttl":null,"direct_redirect":null`, true},
	} {
		s.Run(testCase.name, func() {
			s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(newTestLink(), nil)
			s.linkManager.EXPECT().
				Update(gomock.Any(), testLinkID, "original", uuid.Nil, "https://example.com/new", gomock.Any()).
				DoAndReturn(func(
					_ context.Context, _, _ string, _ uuid.UUID, _ string, opts links.UpdateOptions,
				) (*domain.Link, error) {
					s.Equal(testCase.clear, opts.ClearExpiry)
					s.Equal(testCase.clear, opts.ResetDirectRedirect)
					s.Nil(opts.ExpiresAt)
					s.Nil(opts.DirectRedirect)
					s.Nil(opts.Privacy)

					return newTestLink(), nil
				})
			r := httptest.NewRequest(http.MethodPut, "/links/"+testLinkID,
				strings.NewReader(`{"target_url":"https://example.com/new","edit_code":"original"`+testCase.fields+`}`))

			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			s.Equal(http.StatusOK, w.Code)
			s.NotContains(w.Body.String(), "edit_code")
		})
	}
}

func (s *HandlerSuite) TestUpdateLink_InvalidBody() {
	for _, body := range []string{
		`{}`, `{"target_url":null}`, `{"target_url":"https://example.com","ttl":-1}`,
		`{"target_url":"https://example.com","ttl":9223372037}`,
		`{"target_url":"https://example.com","privacy":"invalid"}`,
		`{"target_url":"https://example.com","direct_redirect":"yes"}`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/links/"+testLinkID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusBadRequest, w.Code, body)
	}
}

func (s *HandlerSuite) TestUpdateLink_AnonymousPrivateLinkDenied() {
	// The anonymous mutation gate applies only with the account system.
	s.router = s.newRouterWithTOTPAuth()
	link := newTestLink()
	privacy := domain.PrivacyAuthenticated

	link.Privacy = &privacy
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(link, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/links/"+testLinkID,
		strings.NewReader(`{"target_url":"https://example.com/new","edit_code":"correct"}`))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestUpdateLink_FailedCodesLockOut() {
	// The anonymous mutation gate applies only with the account system.
	s.router = s.newRouterWithTOTPAuth()
	s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(newTestLink(), nil).Times(10)
	s.linkManager.EXPECT().Update(gomock.Any(), testLinkID, "wrong", uuid.Nil, gomock.Any(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode).Times(10)

	for i := range 11 {
		w := httptest.NewRecorder()

		r := httptest.NewRequest(http.MethodPut, "/links/"+testLinkID,
			strings.NewReader(`{"target_url":"https://example.com/new","edit_code":"wrong"}`))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)

		if i < 10 {
			s.Equal(http.StatusForbidden, w.Code)
		} else {
			s.Equal(http.StatusTooManyRequests, w.Code)
		}
	}
}
