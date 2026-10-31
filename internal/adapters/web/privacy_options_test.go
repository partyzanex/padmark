package web

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestPrivacyOptions pins which privacy levels the editor offers (review R2).
func TestPrivacyOptions(t *testing.T) {
	t.Parallel()

	const notEnforced = "Not enforced: accounts are off, anyone with the link can view"

	values := func(options []privacyOption) []string {
		result := make([]string, 0, len(options))
		for _, option := range options {
			result = append(result, option.Value)
		}

		return result
	}

	cases := []struct {
		name             string
		accounts, signed bool
		current          domain.Privacy
		want             []string
	}{
		{"no accounts: public only", false, false, domain.PrivacyPublic, []string{"public"}},
		{"accounts, anonymous: no owner", true, false, domain.PrivacyPublic, []string{"public", "authenticated"}},
		{"accounts, signed in: all", true, true, domain.PrivacyPublic, []string{"public", "authenticated", "owner"}},
		{
			"no accounts, editing a v0.1.6 private note", false, false, domain.PrivacyAuthenticated,
			[]string{"public", "authenticated"},
		},
	}

	for _, tcase := range cases {
		assert.Equal(t, tcase.want, values(privacyOptions(tcase.accounts, tcase.signed, tcase.current)), tcase.name)
	}

	edited := privacyOptions(false, false, domain.PrivacyAuthenticated)
	assert.Equal(t, privacyOption{Value: "authenticated", Label: "Requires login", Hint: notEnforced, Selected: true},
		edited[1], "the kept level says it is not enforced")
	assert.False(t, edited[0].Selected)
	assert.Empty(t, privacyOptions(true, false, domain.PrivacyAuthenticated)[1].Hint,
		"enforced levels keep the default hint")
}
