package netx

import (
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"sync/atomic"
)

// cacert.pem is Mozilla's public root program, extracted by curl.se.
// It is the ONLY built-in trust source: the Windows certificate store is never consulted.
//
//go:embed cacert.pem
var mozillaBundle []byte

var builtinRoots []*x509.Certificate

func init() {
	rest := mozillaBundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(block.Bytes); err == nil {
			builtinRoots = append(builtinRoots, c)
		}
	}
}

// BuiltinRootCount reports how many public roots are embedded.
func BuiltinRootCount() int { return len(builtinRoots) }

var currentPool atomic.Pointer[x509.CertPool]

func init() { SetExtraRoots(nil) }

// SetExtraRoots rebuilds the trust pool as Mozilla roots + the given user CAs.
func SetExtraRoots(extra []*x509.Certificate) {
	p := x509.NewCertPool()
	for _, c := range builtinRoots {
		p.AddCert(c)
	}
	for _, c := range extra {
		p.AddCert(c)
	}
	currentPool.Store(p)
}

func rootPool() *x509.CertPool { return currentPool.Load() }

// ParseCertificates accepts PEM (one or more blocks) or a single DER certificate.
func ParseCertificates(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		c, err := x509.ParseCertificate(data)
		if err != nil {
			return nil, errors.New("无法识别的证书文件（需要 PEM 或 DER 格式）")
		}
		out = append(out, c)
	}
	return out, nil
}

// Fingerprint returns the colon separated SHA-256 fingerprint of a certificate.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// CertToPEM encodes a certificate as PEM.
func CertToPEM(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

// DisplayName picks a human readable name for a certificate subject.
func DisplayName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.Subject.Organization) > 0 {
		return c.Subject.Organization[0]
	}
	return c.Subject.String()
}
