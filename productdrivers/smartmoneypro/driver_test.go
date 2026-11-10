package smartmoneypro

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/quant-store/quantstore-cli/driverapi"
)

type fakeSources map[string]SourceSpec

func (sources fakeSources) Lookup(name string) (SourceSpec, bool) {
	spec, exists := sources[name]
	return spec, exists
}

func TestOperationIDsMatchWhaleDeveloperCatalog(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate driver test source")
	}
	catalogPath := filepath.Clean(filepath.Join(
		filepath.Dir(thisFile),
		"testdata/developer_catalog.json",
	))
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read authoritative Whale developer catalog: %v", err)
	}
	var catalog struct {
		Operations []struct {
			OperationID string `json:"operation_id"`
			Protocol    string `json:"protocol"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("parse authoritative Whale developer catalog: %v", err)
	}

	want := make([]string, 0, len(catalog.Operations))
	counts := make(map[string]int)
	seen := make(map[string]struct{}, len(catalog.Operations))
	for _, operation := range catalog.Operations {
		if operation.OperationID == "" {
			t.Fatal("authoritative Whale developer catalog contains an empty operation_id")
		}
		if _, duplicate := seen[operation.OperationID]; duplicate {
			t.Fatalf("authoritative Whale developer catalog contains duplicate operation_id %q", operation.OperationID)
		}
		seen[operation.OperationID] = struct{}{}
		want = append(want, operation.OperationID)
		counts[operation.Protocol]++
	}
	if len(want) != 39 || counts["rest"] != 31 || counts["mcp"] != 7 || counts["wss"] != 1 || len(counts) != 3 {
		t.Fatalf("authoritative catalog counts=%v total=%d; want rest=31 mcp=7 wss=1 total=39", counts, len(want))
	}
	sort.Strings(want)
	if got := New(nil).OperationIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("operation IDs=%v want=%v", got, want)
	}
}

func TestSubjectNormalizationUsesRuntimeSourceDescriptor(t *testing.T) {
	sources := fakeSources{
		"evm":    {Name: "evm", Normalize: "lower"},
		"solana": {Name: "solana", Normalize: "verbatim"},
	}
	handler, _ := New(sources).Handler("profile.get")
	for _, test := range []struct {
		source, subject, want string
	}{
		{" EVM ", " 0xAbC ", "0xabc"},
		{"solana", " AbCdEF ", "AbCdEF"},
	} {
		call, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{
			Query: url.Values{"source": {test.source}, "subject": {test.subject}},
		}, driverapi.OperationView{})
		if driverErr != nil {
			t.Fatal(driverErr)
		}
		if got := call.Query.Get("subject"); got != test.want {
			t.Fatalf("subject = %q, want %q", got, test.want)
		}
	}
}

func TestUnknownRuntimeSourceIsRejected(t *testing.T) {
	handler, _ := New(fakeSources{"evm": {Name: "evm", Normalize: "lower"}}).Handler("profile.get")
	_, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{
		Query: url.Values{"source": {"bitcoin"}, "subject": {"abc"}},
	}, driverapi.OperationView{})
	if driverErr == nil || driverErr.Code != "unknown_source" {
		t.Fatalf("error = %#v", driverErr)
	}
}

func TestNoRuntimeDescriptorDoesNotGuessSubjectSemantics(t *testing.T) {
	handler, _ := New(nil).Handler("profile.get")
	call, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{
		Query: url.Values{"source": {" EVM "}, "subject": {" 0xAbC "}},
	}, driverapi.OperationView{})
	if driverErr != nil {
		t.Fatal(driverErr)
	}
	if got := call.Query.Get("subject"); got != "0xAbC" {
		t.Fatalf("subject = %q", got)
	}
}
