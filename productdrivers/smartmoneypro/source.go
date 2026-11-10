package smartmoneypro

import "strings"

type SourceSpec struct {
	Name        string
	SubjectKind string
	Normalize   string
}

type SourceCatalog interface {
	Lookup(name string) (SourceSpec, bool)
}

func normalizeSubject(spec SourceSpec, subject string) string {
	canonical := strings.TrimSpace(subject)
	if spec.Normalize == "lower" {
		canonical = strings.ToLower(canonical)
	}
	return canonical
}
