package smartmoneypro

import (
	"context"
	"strings"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func subjectHandler(operationID string, sources SourceCatalog) driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			cloned := driverapi.CloneRequest(request)
			call := driverapi.PreparedCall{PathParams: cloned.PathParams, Query: cloned.Query, Body: cloned.Body}
			source := call.Query.Get("source")
			subject := call.Query.Get("subject")
			canonicalSource := strings.ToLower(strings.TrimSpace(source))
			warnings := make([]driverapi.Warning, 0, 2)
			if canonicalSource != source {
				call.Query.Set("source", canonicalSource)
				warnings = append(warnings, driverapi.Warning{Code: "input_canonicalized", Field: "query.source", From: source, To: canonicalSource})
			}
			canonicalSubject := strings.TrimSpace(subject)
			if sources != nil && canonicalSource != "" {
				spec, exists := sources.Lookup(canonicalSource)
				if !exists {
					return driverapi.PreparedCall{}, nil, driverapi.NewError("unknown_source", "source is not present in the runtime source registry")
				}
				canonicalSubject = normalizeSubject(spec, subject)
			}
			if canonicalSubject != subject {
				call.Query.Set("subject", canonicalSubject)
				warnings = append(warnings, driverapi.Warning{Code: "input_canonicalized", Field: "query.subject", From: subject, To: canonicalSubject})
			}
			return call, warnings, nil
		},
	}
}
