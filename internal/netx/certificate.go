package netx

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

// CertificateInfo describes this origin's most recently observed upstream TLS
// handshake, not the proxy's session certificate shown to WebView2. It is kept
// only in memory; reading it never makes a new connection.
type CertificateInfo struct {
	Host          string               `json:"host"`
	RemoteAddress string               `json:"remoteAddress"`
	ObservedAt    time.Time            `json:"observedAt"`
	Verified      bool                 `json:"verified"`
	Error         string               `json:"error,omitempty"`
	TLSVersion    string               `json:"tlsVersion,omitempty"`
	TrustSource   string               `json:"trustSource,omitempty"`
	TrustAnchor   *CertificateDetails  `json:"trustAnchor,omitempty"`
	Chain         []CertificateDetails `json:"chain"`
}

type CertificateDetails struct {
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	Serial      string   `json:"serial"`
	DNSNames    []string `json:"dnsNames"`
	NotBefore   string   `json:"notBefore"`
	NotAfter    string   `json:"notAfter"`
	Fingerprint string   `json:"fingerprint"`
}

func certificateDetails(c *x509.Certificate) CertificateDetails {
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	return CertificateDetails{
		Subject: c.Subject.String(), Issuer: c.Issuer.String(), Serial: strings.ToUpper(c.SerialNumber.Text(16)),
		DNSNames: names, NotBefore: c.NotBefore.Format(time.RFC3339), NotAfter: c.NotAfter.Format(time.RFC3339), Fingerprint: Fingerprint(c),
	}
}

func certificateKey(host, port string) string {
	host = normHost(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, port)
}

func (p *Proxy) recordTLS(host, port, remote string, state tls.ConnectionState, handshakeErr error) {
	info := &CertificateInfo{Host: certificateKey(host, port), RemoteAddress: remote, ObservedAt: time.Now(),
		Verified: handshakeErr == nil && len(state.VerifiedChains) > 0, Chain: []CertificateDetails{}}
	peer := state.PeerCertificates
	if handshakeErr != nil {
		info.Error = handshakeErr.Error()
		var verifyErr *tls.CertificateVerificationError
		if errors.As(handshakeErr, &verifyErr) {
			peer = verifyErr.UnverifiedCertificates
		}
	}
	if state.Version != 0 {
		info.TLSVersion = tls.VersionName(state.Version)
	}
	for _, cert := range peer {
		info.Chain = append(info.Chain, certificateDetails(cert))
	}
	if info.Verified {
		chain := state.VerifiedChains[0]
		if len(chain) > 0 {
			root := chain[len(chain)-1]
			anchor := certificateDetails(root)
			info.TrustAnchor = &anchor
			info.TrustSource = "imported"
			for _, builtin := range builtinRoots {
				if bytes.Equal(root.Raw, builtin.Raw) {
					info.TrustSource = "builtin"
					break
				}
			}
		}
	}
	p.certMu.Lock()
	defer p.certMu.Unlock()
	if p.certificates == nil {
		p.certificates = make(map[string]*CertificateInfo)
	}
	key := certificateKey(host, port)
	if p.certificates[key] == nil && len(p.certificates) >= 256 {
		var oldest string
		for k, entry := range p.certificates {
			if oldest == "" || entry.ObservedAt.Before(p.certificates[oldest].ObservedAt) {
				oldest = k
			}
		}
		delete(p.certificates, oldest)
	}
	p.certificates[key] = info
}

// Certificate returns a detached snapshot for an HTTPS origin, including its
// port. HTTP pages and origins with no observed handshake have no certificate.
func (p *Proxy) Certificate(rawURL string) *CertificateInfo {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return nil
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	p.certMu.Lock()
	defer p.certMu.Unlock()
	info := p.certificates[certificateKey(u.Hostname(), port)]
	if info == nil {
		return nil
	}
	cloneDetails := func(c CertificateDetails) CertificateDetails {
		c.DNSNames = append([]string{}, c.DNSNames...)
		return c
	}
	out := *info
	out.Chain = make([]CertificateDetails, len(info.Chain))
	for i, cert := range info.Chain {
		out.Chain[i] = cloneDetails(cert)
	}
	if info.TrustAnchor != nil {
		anchor := cloneDetails(*info.TrustAnchor)
		out.TrustAnchor = &anchor
	}
	return &out
}
