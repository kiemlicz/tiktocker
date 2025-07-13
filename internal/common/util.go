package common

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
)

func WaitForResult(ctx context.Context, ch <-chan *RequestResult) *RequestResult {
	select {
	case v, ok := <-ch:
		if !ok {
			return &RequestResult{Err: fmt.Errorf("channel closed")}
		}
		return v
	case <-ctx.Done():
		Log.Debugf("context done: %v", ctx.Err())
		return &RequestResult{Err: fmt.Errorf("context done: %v", ctx.Err())}
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

func ComputeSha256(contents []byte) string {
	sum := sha256.Sum256(contents)
	return base64.StdEncoding.EncodeToString(sum[:])
}
