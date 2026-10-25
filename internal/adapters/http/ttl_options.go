package http

import "fmt"

type ttlOption struct {
	Label    string
	Value    int64
	Selected bool
}

// burnTTLOptions preserves an existing duration even when it is not one of the editor presets.
func burnTTLOptions(current int64) []ttlOption {
	presets := [...]ttlOption{
		{"Immediately", 0, false},
		{"5 minutes", 300, false},
		{"10 minutes", 600, false},
		{"30 minutes", 1800, false},
		{"1 hour", 3600, false},
		{"4 hours", 14400, false},
		{"12 hours", 43200, false},
		{"24 hours", 86400, false},
		{"3 days", 259200, false},
		{"7 days", 604800, false},
		{"30 days", 2592000, false},
		{"90 days", 7776000, false},
	}
	for index := range presets {
		if presets[index].Value == current {
			presets[index].Selected = true

			return presets[:]
		}
	}

	return append(presets[:], ttlOption{Label: fmt.Sprintf("%d seconds", current), Value: current, Selected: true})
}
