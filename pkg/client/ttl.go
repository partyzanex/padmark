package client

import (
	"math"
	"time"
)

// This file is not generated. Keep it out of the ogen output names (oas_*_gen.go): ogen --clean
// removes only those files.

// MaxTTLSeconds is the largest TTL in seconds that the API accepts: the "maximum" of each ttl
// field in openapi.yaml. It is the largest number of seconds that time.Duration can hold, so a
// client can convert each valid TTL to a time.Duration without an overflow.
const MaxTTLSeconds int64 = math.MaxInt64 / int64(time.Second)
