package secret

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeStore struct {
	value string
	err   error
}

func (f *fakeStore) Get(context.Context, string) (string, error) { return f.value, f.err }
func (f *fakeStore) Set(context.Context, string, string) error   { return nil }
func (f *fakeStore) Delete(context.Context, string) error        { return nil }

func TestValidateExactGeneratedShape(t *testing.T) {
	valid := "qs_live_" + strings.Repeat("A", 43)
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"qs_live_" + strings.Repeat("A", 42),
		"qs_live_" + strings.Repeat("A", 44),
		" qs_live_" + strings.Repeat("A", 43),
		"qs_test_" + strings.Repeat("A", 43),
	} {
		if err := Validate(invalid); err == nil {
			t.Fatalf("accepted invalid key shape %q", invalid)
		}
	}
}

func TestResolveEnvironmentPrecedesKeychain(t *testing.T) {
	envKey := "qs_live_" + strings.Repeat("E", 43)
	t.Setenv(EnvironmentVariable, envKey)
	value, source, err := Resolve(context.Background(), "default", &fakeStore{err: errors.New("must not be called")})
	if err != nil {
		t.Fatal(err)
	}
	if value != envKey || source != "environment" {
		t.Fatalf("got value=%q source=%q", value, source)
	}
}

func TestResolveUsesKeychainWhenEnvironmentIsAbsent(t *testing.T) {
	old, existed := os.LookupEnv(EnvironmentVariable)
	if err := os.Unsetenv(EnvironmentVariable); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(EnvironmentVariable, old)
		} else {
			_ = os.Unsetenv(EnvironmentVariable)
		}
	})
	stored := "qs_live_" + strings.Repeat("K", 43)
	value, source, err := Resolve(context.Background(), "default", &fakeStore{value: stored})
	if err != nil {
		t.Fatal(err)
	}
	if value != stored || source != "keychain" {
		t.Fatalf("got value=%q source=%q", value, source)
	}
}

func TestKeyringStoreHonorsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (KeyringStore{}).Get(ctx, "default")
	var backendErr *BackendError
	if !errors.As(err, &backendErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled BackendError, got %v", err)
	}
}
