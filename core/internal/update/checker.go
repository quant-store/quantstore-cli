package update

import "context"

// Checker is intentionally an interface only until the releases TUF role and
// platform bootstrap contract exist. A plain .sum implementation is forbidden.
type Checker interface {
	Check(ctx context.Context, force bool) (Status, error)
}

type Status struct {
	CurrentVersion string
	LatestVersion  string
	UpdateReady    bool
}
