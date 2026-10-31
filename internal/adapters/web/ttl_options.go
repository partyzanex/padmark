package web

import "fmt"

// Time units in seconds for the burn-after-reading presets.
const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	secondsPerDay    = 24 * secondsPerHour
)

// Burn-after-reading presets in seconds, in the order the editor offers them.
const (
	ttlFiveMinutes   = 5 * secondsPerMinute
	ttlTenMinutes    = 10 * secondsPerMinute
	ttlThirtyMinutes = 30 * secondsPerMinute
	ttlOneHour       = secondsPerHour
	ttlFourHours     = 4 * secondsPerHour
	ttlTwelveHours   = 12 * secondsPerHour
	ttlOneDay        = secondsPerDay
	ttlThreeDays     = 3 * secondsPerDay
	ttlSevenDays     = 7 * secondsPerDay
	ttlThirtyDays    = 30 * secondsPerDay
	ttlNinetyDays    = 90 * secondsPerDay
)

type ttlOption struct {
	Label    string
	Value    int64
	Selected bool
}

// burnTTLOptions preserves an existing duration even when it is not one of the editor presets.
func burnTTLOptions(current int64) []ttlOption {
	presets := [...]ttlOption{
		{"Immediately", 0, false},
		{"5 minutes", ttlFiveMinutes, false},
		{"10 minutes", ttlTenMinutes, false},
		{"30 minutes", ttlThirtyMinutes, false},
		{"1 hour", ttlOneHour, false},
		{"4 hours", ttlFourHours, false},
		{"12 hours", ttlTwelveHours, false},
		{"24 hours", ttlOneDay, false},
		{"3 days", ttlThreeDays, false},
		{"7 days", ttlSevenDays, false},
		{"30 days", ttlThirtyDays, false},
		{"90 days", ttlNinetyDays, false},
	}
	for index := range presets {
		if presets[index].Value == current {
			presets[index].Selected = true

			return presets[:]
		}
	}

	return append(presets[:], ttlOption{Label: fmt.Sprintf("%d seconds", current), Value: current, Selected: true})
}
