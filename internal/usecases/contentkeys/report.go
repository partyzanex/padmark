package contentkeys

// Report contains counts and public key IDs; never content or key bytes. Only a prune dry run
// fills Candidates with record IDs, so the operator can review them before any deletion; for an
// untagged v0.1.6 note the ID is its slug. CorruptFile counts file records whose sealed metadata
// has no version tag; they are also Failed.
type Report struct {
	Keys           map[string]int `json:"keys"`
	State          string         `json:"state"`
	Candidates     []Candidate    `json:"candidates,omitempty"`
	Raw            int            `json:"raw"`
	Wrapped        int            `json:"wrapped"`
	Native         int            `json:"native"`
	Failed         int            `json:"failed"`
	CorruptRaw     int            `json:"corrupt_raw"`
	UntaggedLegacy int            `json:"untagged_legacy"`
	CorruptFile    int            `json:"corrupt_file"`
	Plaintext      int            `json:"plaintext"`
	Pruned         int            `json:"pruned"`
	Changed        int            `json:"changed"`
	Conflicts      int            `json:"conflicts"`
	Missing        int            `json:"missing"`
}

// Candidate is one record a prune dry run reports: Reason is "corrupt_raw" (prune would delete
// it), "untagged_legacy" (a v0.1.6 note that prune never deletes; tag it with MarkLegacy) or
// "corrupt_file" (a file record that prune never deletes; restore it or remove it and its object
// by hand; the files row with this ID names the object in its object_key column).
type Candidate struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Reason   string `json:"reason"`
}

func newReport(state string) Report {
	var report Report

	report.State = state
	report.Keys = make(map[string]int)

	return report
}
