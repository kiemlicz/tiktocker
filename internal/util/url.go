package util

import "net/url"

// move to some kind of util??

func CreateUrl(host string, username string, password string) (*url.URL, error) {
	//todo, some more?

	u := &url.URL{
		Scheme: "http", //todo https handling
		Host:   host,
		User:   url.UserPassword(username, password),
	}

	return u, nil
}
