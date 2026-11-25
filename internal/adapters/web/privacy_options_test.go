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

	// rows gives each row as its value, with a "-" suffix for a disabled row.
	rows := func(options []privacyOption) []string {
		result := make([]string, 0, len(options))
		for _, option := range options {
			row := option.Value
			if option.Disabled {
				row += "-"
			}

			result = append(result, row)
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
		{
			"accounts, anonymous: protected levels disabled", true, false, domain.PrivacyPublic,
			[]string{"public", "authenticated-", "owner-"},
		},
		{"accounts, signed in: all", true, true, domain.PrivacyPublic, []string{"public", "authenticated", "owner"}},
		{
			"accounts, anonymous, editing a login-required record: the current level stays available",
			true, false, domain.PrivacyAuthenticated, []string{"public", "authenticated", "owner-"},
		},
		{
			"no accounts, editing a v0.1.6 private note", false, false, domain.PrivacyAuthenticated,
			[]string{"public", "authenticated"},
		},
	}

	for _, tcase := range cases {
		assert.Equal(t, tcase.want,
			rows(privacyOptions(accountMode(tcase.accounts), tcase.signed, tcase.current)), tcase.name)
	}

	edited := privacyOptions(false, false, domain.PrivacyAuthenticated)
	assert.Equal(t, privacyOption{
		Value: "authenticated", Label: "Login required", Hint: notEnforced, Selected: true, Disabled: false,
	}, edited[1], "the kept level says it is not enforced")
	assert.False(t, edited[0].Selected)
	assert.Empty(t, privacyOptions(true, false, domain.PrivacyAuthenticated)[1].Hint,
		"enforced levels keep the default hint")
}

// TestNewPrivacyChoice: the sign-in link is present only when a row is disabled, and it returns
// the user to the form.
func TestNewPrivacyChoice(t *testing.T) {
	t.Parallel()

	anonymous := newPrivacyChoice(accountsOn, false, domain.PrivacyPublic, "/edit/abc?x=1")
	assert.Equal(t, "/login?next=%2Fedit%2Fabc%3Fx%3D1", anonymous.SignInURL)
	assert.Len(t, anonymous.Options, 3)

	assert.Empty(t, newPrivacyChoice(accountsOn, true, domain.PrivacyPublic, "/").SignInURL,
		"a signed-in user has no disabled row")
	assert.Empty(t, newPrivacyChoice(accountsOff, false, domain.PrivacyPublic, "/").SignInURL,
		"without accounts sign-in does not enable a level")
}
