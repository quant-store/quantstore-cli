package exitcode

import (
	"errors"
	"net"

	"github.com/quant-store/quantstore-cli/core/internal/transport/httpx"
)

const (
	OK          = 0
	Local       = 2
	Auth        = 3
	Access      = 4
	RateLimited = 5
	NotFound    = 6
	Network     = 7
	Remote      = 8
	Trust       = 9
)

type Coded interface {
	ExitCode() int
}

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) ExitCode() int { return e.Code }

func New(code int, message string) error {
	return &Error{Code: code, Message: message}
}

func From(err error) int {
	if err == nil {
		return OK
	}
	var coded Coded
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return Auth
		case 402, 403:
			return Access
		case 404:
			return NotFound
		case 429:
			return RateLimited
		default:
			if apiErr.StatusCode >= 500 {
				return Remote
			}
			return Local
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return Network
	}
	return Local
}
