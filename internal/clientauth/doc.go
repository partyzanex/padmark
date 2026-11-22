// Package clientauth holds the client-side rules of padmark-cli and padmark-mcp for the server
// address and the credentials: the token file, the trust-on-first-use record of pmk_ envelope
// origins, the plain-HTTP policy and the HTTP client with the redirect guard. NewClient builds
// the one API client of a binary run from these rules.
//
// The two binaries had one copy each of this code, and the copies drifted. A security rule
// must have one owner, so both adapters use this package.
//
// Non-goals: the package does not map API responses to errors (pkg/client APIError does that)
// and does not read command-line flags. Each adapter reads its own flags into Settings.
package clientauth
