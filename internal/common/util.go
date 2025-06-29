package common

import (
	"context"
	"fmt"
	"net/url"
)

// fixme move to util, have a type with required fields here
// WaitForResult waits for a value from the channel or context cancellation.
func WaitForResult[T any](ctx context.Context, ch <-chan T) (T, error) {
	var zero T
	select {
	case v, ok := <-ch:
		if !ok {
			return zero, fmt.Errorf("channel closed")
		}
		return v, nil
	case <-ctx.Done():
		Log.Debugf("context done: %v", ctx.Err())
		return zero, fmt.Errorf("context done: %v", ctx.Err())
	}
}

func CreateUrl(host string, username string, password string) (*url.URL, error) {
	//todo, some more?

	u := &url.URL{
		Scheme: "http", //todo https handling
		Host:   host,
		User:   url.UserPassword(username, password),
	}

	return u, nil
}
