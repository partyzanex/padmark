//go:build integration

package upgrade_test

// Fixed test credentials and expected notes from the real v0.3 release fixtures.
//
//nolint:gosec,funlen // fixed historical fixture data; all credentials belong to disposable test databases
func v03Fixtures() map[string]v03Fixture {
	return map[string]v03Fixture{

		"v0.3.2/sqlite": {
			Version: "v0.3.2",
			Auth: v03Auth{
				Username:   "compatadmin",
				Password:   "Upgrade-fixture-Passphrase-2026!",
				TOTPSecret: "GURSLLYBGZFZVDB7HFB4TBD7AJMFBPAE",
				APIToken:   "u0EJjoLfxCTf1O8sOtZ2Bm9LhYoqdJV_csY7u8H0FYc",
				Session:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Notes: []v03Note{

				{
					Name:     "unicode",
					ID:       "compat-unicode",
					EditCode: "k8wtL6IokNqO",
					Content:  "# Привет 🌍\nPrice: $42; `echo $HOME`"},

				{
					Name:     "plain",
					ID:       "compat-plain",
					EditCode: "IghI8o0a1iuG",
					Content:  "plain <b>text</b>"},

				{
					Name:     "private",
					ID:       "compat-private",
					EditCode: "3swsFnAcJr0j",
					Content:  "private fixture"},

				{
					Name:     "burn",
					ID:       "compat-burn",
					EditCode: "EGBNeLiJLYNP",
					Content:  "read once"},

				{
					Name:     "grace",
					ID:       "compat-grace",
					EditCode: "AQrgBYpskKTn",
					Content:  "custom grace"},

				{
					Name:     "timer",
					ID:       "compat-timer",
					EditCode: "xxo7MYa1mOSd",
					Content:  "armed long timer"},

				{
					Name:     "weak_code",
					ID:       "compat-weak_code",
					EditCode: "hunter2",
					Content:  "historical weak code"},
			}},

		"v0.3.2/postgres": {
			Version: "v0.3.2",
			Auth: v03Auth{
				Username:   "compatadmin",
				Password:   "Upgrade-fixture-Passphrase-2026!",
				TOTPSecret: "2BXJSONMRV7OGSO34DBKDLS75SAYJLZ7",
				APIToken:   "wJTWKcPJGeYTFrZaVqEUg149RVbFEhm_q0tgRwF_8vs",
				Session:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Notes: []v03Note{

				{
					Name:     "unicode",
					ID:       "compat-unicode",
					EditCode: "vYGQb8KX8OTq",
					Content:  "# Привет 🌍\nPrice: $42; `echo $HOME`"},

				{
					Name:     "plain",
					ID:       "compat-plain",
					EditCode: "EQFT2g7O6YpA",
					Content:  "plain <b>text</b>"},

				{
					Name:     "private",
					ID:       "compat-private",
					EditCode: "8HGVAABiBL14",
					Content:  "private fixture"},

				{
					Name:     "burn",
					ID:       "compat-burn",
					EditCode: "4MsPCigkB15s",
					Content:  "read once"},

				{
					Name:     "grace",
					ID:       "compat-grace",
					EditCode: "RL6gqQHpHrvc",
					Content:  "custom grace"},

				{
					Name:     "timer",
					ID:       "compat-timer",
					EditCode: "ceNg47rWTMRc",
					Content:  "armed long timer"},

				{
					Name:     "weak_code",
					ID:       "compat-weak_code",
					EditCode: "hunter2",
					Content:  "historical weak code"},
			}},

		"v0.3.3/sqlite": {
			Version: "v0.3.3",
			Auth: v03Auth{
				Username:   "compatadmin",
				Password:   "Upgrade-fixture-Passphrase-2026!",
				TOTPSecret: "J5WGLA56WR7NWXMAECRXHIGLSM6PKOK3",
				APIToken:   "qRCMeVad5CUDGug2wqtIcTA2n2rBPKJEio5HcrpoVC8",
				Session:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Notes: []v03Note{

				{
					Name:     "unicode",
					ID:       "compat-unicode",
					EditCode: "mxBhB9yZGeIo",
					Content:  "# Привет 🌍\nPrice: $42; `echo $HOME`"},

				{
					Name:     "plain",
					ID:       "compat-plain",
					EditCode: "6FPzN0llKMy6",
					Content:  "plain <b>text</b>"},

				{
					Name:     "private",
					ID:       "compat-private",
					EditCode: "e4SvftPKjVJ7",
					Content:  "private fixture"},

				{
					Name:     "burn",
					ID:       "compat-burn",
					EditCode: "PJ9uPgDz2MKv",
					Content:  "read once"},

				{
					Name:     "grace",
					ID:       "compat-grace",
					EditCode: "ldJzSsZk8TqB",
					Content:  "custom grace"},

				{
					Name:     "timer",
					ID:       "compat-timer",
					EditCode: "gMp7pI1QOF3i",
					Content:  "armed long timer"},

				{
					Name:     "weak_code",
					ID:       "compat-weak_code",
					EditCode: "hunter2",
					Content:  "historical weak code"},
			}},

		"v0.3.3/postgres": {
			Version: "v0.3.3",
			Auth: v03Auth{
				Username:   "compatadmin",
				Password:   "Upgrade-fixture-Passphrase-2026!",
				TOTPSecret: "CEXGXUGECDZMODS54SZGBX7SU2U6KAI5",
				APIToken:   "szB7a6i7ynUKU6fJnK3vVyNfmLlbGhMogkYDkF7f_UE",
				Session:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Notes: []v03Note{

				{
					Name:     "unicode",
					ID:       "compat-unicode",
					EditCode: "Tm1uTO0n85as",
					Content:  "# Привет 🌍\nPrice: $42; `echo $HOME`"},

				{
					Name:     "plain",
					ID:       "compat-plain",
					EditCode: "1zOgeeJtbkSs",
					Content:  "plain <b>text</b>"},

				{
					Name:     "private",
					ID:       "compat-private",
					EditCode: "G9qFFLeeMCcx",
					Content:  "private fixture"},

				{
					Name:     "burn",
					ID:       "compat-burn",
					EditCode: "8IYiQ3V4plZC",
					Content:  "read once"},

				{
					Name:     "grace",
					ID:       "compat-grace",
					EditCode: "PXgABLQpYSeP",
					Content:  "custom grace"},

				{
					Name:     "timer",
					ID:       "compat-timer",
					EditCode: "3Hi2OFy9lHRg",
					Content:  "armed long timer"},

				{
					Name:     "weak_code",
					ID:       "compat-weak_code",
					EditCode: "hunter2",
					Content:  "historical weak code"},
			}},
	}
}
