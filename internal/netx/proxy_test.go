package netx

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// client talks to the proxy the way the browser does: it trusts only the
// proxy's session CA.
func proxyClient(t *testing.T, p *Proxy) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(p.ca.caCert)
	pu, _ := url.Parse("http://" + p.Addr())
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

func TestCustomDNSAndCA(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello from "+r.Host)
	}))
	defer upstream.Close()
	_, port, _ := net.SplitHostPort(upstream.Listener.Addr().String())

	// httptest's cert is valid for example.com
	SetRecords([][]Record{{
		{Host: "example.com", Type: "A", Value: "127.0.0.1"},
	}})
	defer SetRecords(nil)
	SetExtraRoots(nil)

	p, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	c := proxyClient(t, p)

	// unknown CA -> error page (502)
	resp, err := c.Get("https://example.com:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(body), "证书不受信任") {
		t.Fatalf("expected untrusted error page, got %d %q", resp.StatusCode, body[:min(200, len(body))])
	}
	address := "https://example.com:" + port + "/"
	info := p.Certificate(address)
	if info == nil || info.Verified || info.Error == "" || len(info.Chain) == 0 ||
		info.Chain[0].Fingerprint != Fingerprint(upstream.Certificate()) || info.TrustAnchor != nil ||
		info.RemoteAddress != upstream.Listener.Addr().String() || info.ObservedAt.IsZero() {
		t.Fatalf("rejected upstream certificate: %+v", info)
	}
	if p.Certificate("http://example.com:"+port) != nil || p.Certificate("https://example.com:1") != nil ||
		p.Certificate("https://other.example.com:"+port) != nil {
		t.Fatal("certificate must match the HTTPS origin, including its port")
	}
	// A caller cannot alter the shared snapshot or its certificate fields.
	info.Chain[0].Subject = "changed"
	info.Chain[0].DNSNames[0] = "changed"
	if again := p.Certificate("https://EXAMPLE.COM.:" + port); again.Chain[0].Subject == "changed" || again.Chain[0].DNSNames[0] == "changed" {
		t.Fatal("certificate snapshot shares mutable data")
	}

	// import the CA -> success through CNAME and wildcard
	SetExtraRoots([]*x509.Certificate{upstream.Certificate()})
	defer SetExtraRoots(nil)
	SetRecords([][]Record{{
		{Host: "example.com", Type: "CNAME", Value: "lb.example.com"},
		{Host: "lb.example.com", Type: "A", Value: "127.0.0.1"},
	}})
	resp, err = c.Get("https://example.com:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "hello from") {
		t.Fatalf("expected success, got %d %q", resp.StatusCode, body)
	}
	info = p.Certificate(address)
	if info == nil || !info.Verified || info.Error != "" || info.TrustSource != "imported" ||
		info.TrustAnchor == nil || info.TrustAnchor.Fingerprint != Fingerprint(upstream.Certificate()) {
		t.Fatalf("imported upstream trust anchor: %+v", info)
	}

	// A later failed handshake must replace the earlier success, not retain its
	// green status or trusted anchor. Force a new connection after removing the CA.
	c.Transport.(*http.Transport).CloseIdleConnections()
	SetExtraRoots(nil)
	resp, err = c.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	info = p.Certificate(address)
	if resp.StatusCode != 502 || info.Verified || info.TrustSource != "" || info.TrustAnchor != nil {
		t.Fatalf("stale trusted status after failed handshake: %+v", info)
	}
}

// An absolute https:// request must not reach the reverse proxy, whose
// transport would verify the upstream with the system roots.
func TestAbsoluteHTTPSNotForwarded(t *testing.T) {
	hits := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer upstream.Close()
	p, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET "+upstream.URL+"/ HTTP/1.1\r\nHost: "+upstream.Listener.Addr().String()+"\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || hits != 0 {
		t.Fatalf("status %d, upstream hits %d", resp.StatusCode, hits)
	}
}

func TestResolveWildcard(t *testing.T) {
	SetRecords([][]Record{
		{{Host: "*.corp.local", Type: "A", Value: "10.0.0.9"}, {Host: "x.corp.local", Type: "A", Value: "10.0.0.1"}},
		{{Host: "x.corp.local", Type: "A", Value: "10.9.9.9"}}, // shadowed by the first group
	})
	defer SetRecords(nil)
	ips, err := Resolve(t.Context(), "a.b.corp.local")
	if err != nil || ips[0].String() != "10.0.0.9" {
		t.Fatalf("wildcard: %v %v", ips, err)
	}
	ips, _ = Resolve(t.Context(), "X.corp.local.")
	if len(ips) != 1 || ips[0].String() != "10.0.0.1" {
		t.Fatalf("priority: %v", ips)
	}
}

func TestPublicSite(t *testing.T) {
	if os.Getenv("INTRA_NET_TEST") == "" {
		t.Skip("set INTRA_NET_TEST=1 to hit the internet")
	}
	p, _ := Start()
	resp, err := proxyClient(t, p).Get("https://www.mozilla.org/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatal(resp.Status)
	}
}
