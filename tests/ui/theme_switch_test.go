package ui_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThemeSwitch(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "Instant", run: testThemeSwitchInstant, accounts: false, files: false, allowedStatuses: nil},
	})
}

// switchThemeScript clicks a theme button and returns the CSS transitions that run right after
// the click. getAnimations brings the styles up to date first, so a colour transition that the
// switch starts is in the list.
const switchThemeScript = `() => {
	document.querySelector('.theme-btn[data-theme-value="%s"]').click();
	return document.getAnimations()
		.filter((animation) => animation instanceof CSSTransition)
		.map((animation) => animation.transitionProperty);
}`

// testThemeSwitchInstant checks that every colour of the page changes in the same frame as the
// theme: no element animates to the new colours.
func testThemeSwitchInstant(sess *uiSession) {
	_, link := sess.publishNote()

	for _, path := range []string{"/", link} {
		sess.gotoPage(path)

		for _, theme := range []string{"light", "dark", "dim", "light"} {
			running, ok := sess.eval(fmt.Sprintf(switchThemeScript, theme)).([]any)
			require.True(sess.t, ok, "the transitions must be a list")
			require.Empty(sess.t, running, "%s: switching to %s must not animate", path, theme)
			sess.must(sess.expect.Locator(sess.page.Locator("html")).ToHaveAttribute("data-theme", theme))
		}
	}
}
