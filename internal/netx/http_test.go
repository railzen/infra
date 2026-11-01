package netx

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrustedHTTPClientRoots(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "update metadata") }))
	defer upstream.Close()
	SetExtraRoots(nil)
	defer SetExtraRoots(nil)
	client := TrustedHTTPClient(5 * time.Second)
	defer client.CloseIdleConnections()
	if resp, err := client.Get(upstream.URL); err == nil {
		resp.Body.Close()
		t.Fatal("unknown update-server CA must be rejected")
	}
	SetExtraRoots([]*x509.Certificate{upstream.Certificate()})
	client = TrustedHTTPClient(5 * time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
}
