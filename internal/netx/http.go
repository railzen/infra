package netx

import (
	"crypto/tls"
	"net/http"
	"time"
)

// TrustedHTTPClient uses the same explicit roots and DNS rules as site traffic.
func TrustedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		Proxy: nil, DialContext: Dial, ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{RootCAs: rootPool(), MinVersion: tls.VersionTLS12},
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
	}}
}
