package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	keyring "github.com/zalando/go-keyring"
)

const (
	EnvironmentVariable = "QS_API_KEY"
	keyringService      = "com.quantstore.qs"
)

var (
	ErrNotFound = errors.New("API key not found")
	keyPattern  = regexp.MustCompile(`^qs_live_[A-Za-z0-9_-]{43}$`)
)

type Store interface {
	Get(ctx context.Context, profile string) (string, error)
	Set(ctx context.Context, profile, value string) error
	Delete(ctx context.Context, profile string) error
}

type KeyringStore struct{}

type BackendError struct {
	Operation string
	Err       error
}

func (e *BackendError) Error() string {
	if errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, context.Canceled) {
		message := e.Operation + " OS keychain: operation interrupted"
		if e.Operation != "read from" {
			message += "; the result may be unknown"
		}
		return message
	}
	return e.Operation + " OS keychain: backend unavailable"
}

func (e *BackendError) Unwrap() error { return e.Err }

func (KeyringStore) Get(ctx context.Context, profile string) (string, error) {
	value, err := keyringCall(ctx, func() (string, error) {
		return keyring.Get(keyringService, account(profile))
	})
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", &BackendError{Operation: "read from", Err: err}
	}
	return value, nil
}

func (KeyringStore) Set(ctx context.Context, profile, value string) error {
	if err := Validate(value); err != nil {
		return err
	}
	_, err := keyringCall(ctx, func() (struct{}, error) {
		return struct{}{}, keyring.Set(keyringService, account(profile), value)
	})
	if err != nil {
		return &BackendError{Operation: "write to", Err: err}
	}
	return nil
}

func (KeyringStore) Delete(ctx context.Context, profile string) error {
	_, err := keyringCall(ctx, func() (struct{}, error) {
		return struct{}{}, keyring.Delete(keyringService, account(profile))
	})
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return &BackendError{Operation: "delete from", Err: err}
	}
	return nil
}

func Resolve(ctx context.Context, profile string, store Store) (value, source string, err error) {
	if value, exists := os.LookupEnv(EnvironmentVariable); exists {
		if err := Validate(value); err != nil {
			return "", "", fmt.Errorf("%s is invalid: %w", EnvironmentVariable, err)
		}
		return value, "environment", nil
	}
	value, err = store.Get(ctx, profile)
	if err != nil {
		return "", "", err
	}
	if err := Validate(value); err != nil {
		return "", "", fmt.Errorf("stored API key is invalid: %w", err)
	}
	return value, "keychain", nil
}

func Validate(value string) error {
	if !keyPattern.MatchString(value) {
		return errors.New("expected qs_live_ followed by 43 URL-safe characters")
	}
	return nil
}

func account(profile string) string { return "profile:" + profile }

type keyringResult[T any] struct {
	value T
	err   error
}

func keyringCall[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	operationContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := make(chan keyringResult[T], 1)
	go func() {
		value, err := call()
		result <- keyringResult[T]{value: value, err: err}
	}()
	select {
	case <-operationContext.Done():
		return zero, operationContext.Err()
	case outcome := <-result:
		return outcome.value, outcome.err
	}
}
