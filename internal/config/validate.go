package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Validate checks the settings every component needs to reach the backend.
func (c Config) Validate() error {
	var missing []string
	for _, f := range []struct {
		key   string
		value string
	}{
		{KeyEndpoint, c.Endpoint},
		{KeyBucket, c.Bucket},
		{KeyAccessKeyID, c.AccessKeyID},
		{KeySecretKey, c.SecretAccessKey},
	} {
		if f.value == "" {
			missing = append(missing, f.key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return c.checkEndpoint()
}

// PublicBase returns the address the bucket's public prefix is served at.
//
// It is derived from the endpoint and bucket unless DKWWS_PUBLIC_BASE_URL says
// otherwise, so a plain setup needs one fewer setting and cannot get the two
// out of step.
func (c Config) PublicBase() (string, error) {
	if c.PublicBaseURL != "" {
		u, err := url.Parse(c.PublicBaseURL)
		if err != nil {
			return "", fmt.Errorf("%s: %w", KeyPublicBaseURL, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("%s must be an absolute URL such as https://files.example.com, got %q",
				KeyPublicBaseURL, c.PublicBaseURL)
		}
		return strings.TrimRight(c.PublicBaseURL, "/"), nil
	}

	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return "", fmt.Errorf("%s: %w", KeyEndpoint, err)
	}
	if c.PathStyle {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + c.Bucket
		return u.String(), nil
	}
	u.Host = c.Bucket + "." + u.Host
	return strings.TrimRight(u.String(), "/"), nil
}

// checkEndpoint refuses to send long-lived credentials over plain HTTP unless
// the endpoint is on this machine or the operator opted out explicitly.
func (c Config) checkEndpoint() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("%s: %w", KeyEndpoint, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL such as https://s3.example.com, got %q", KeyEndpoint, c.Endpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if c.AllowInsecure || isLoopback(u.Hostname()) {
			return nil
		}
		return errors.New(KeyEndpoint + " uses http, which would send the secret access key in the clear; use https or set " + KeyAllowInsecure + "=true")
	default:
		return fmt.Errorf("%s must use http or https, got %q", KeyEndpoint, u.Scheme)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
