package netx

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// Proxy is the HTTP proxy every page request of the embedded browser goes through.
// It applies the custom DNS table and validates upstream TLS against the
// embedded Mozilla roots plus user imported CAs only.
type Proxy struct {
	ln           net.Listener
	ca           *localCA
	srv          *http.Server
	rp           *httputil.ReverseProxy
	certMu       sync.Mutex
	certificates map[string]*CertificateInfo
}

// Start listens on a random loopback port.
func Start() (*Proxy, error) {
	ca, err := newLocalCA()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{ln: ln, ca: ca}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         Dial,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		},
		FlushInterval: 100 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErrorPage(w, r.URL.Hostname(), err)
		},
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go p.srv.Serve(ln)
	return p, nil
}

// Addr is the "127.0.0.1:port" the browser should use as proxy.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// SPKIHash identifies the only certificate key the browser should accept.
func (p *Proxy) SPKIHash() string { return p.ca.SPKIHash() }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.handleConnect(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http":
		// only plain http: rp's transport would verify https with the system roots
		p.rp.ServeHTTP(w, r)
	default:
		http.Error(w, "Intra proxy", http.StatusNotFound)
	}
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	hostport := r.Host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(hostport, "443")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		conn.Close()
		return
	}
	bc := &bufConn{Conn: conn, r: brw.Reader}
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	first, err := bc.r.Peek(1)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return
	}
	if first[0] == 0x16 { // TLS handshake record
		p.mitm(bc, hostport)
	} else {
		p.tunnel(bc, hostport)
	}
}

func (p *Proxy) tunnel(client net.Conn, hostport string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := Dial(ctx, "tcp", hostport)
	cancel()
	if err != nil {
		client.Close()
		return
	}
	relay(client, up)
}

func (p *Proxy) mitm(client net.Conn, hostport string) {
	connectHost, port, _ := net.SplitHostPort(hostport)
	var (
		upstream *tls.Conn
		upErr    error
		sni      string
	)
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = hello.ServerName
			if sni == "" {
				sni = connectHost
			}
			upstream, upErr = p.dialUpstreamTLS(sni, port, hello.SupportedProtos)
			protos := []string{"http/1.1"}
			if upErr == nil {
				if np := upstream.ConnectionState().NegotiatedProtocol; np != "" {
					protos = []string{np}
				}
			}
			cert, err := p.ca.certFor(sni)
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   tls.VersionTLS12,
				Certificates: []tls.Certificate{*cert},
				NextProtos:   protos,
			}, nil
		},
	}
	tc := tls.Server(client, cfg)
	tc.SetDeadline(time.Now().Add(45 * time.Second))
	err := tc.Handshake()
	tc.SetDeadline(time.Time{})
	if err != nil {
		tc.Close()
		if upstream != nil {
			upstream.Close()
		}
		return
	}
	if upErr != nil {
		serveErrorOnce(tc, sni, upErr)
		return
	}
	relay(tc, upstream)
}

func (p *Proxy) dialUpstreamTLS(host, port string, clientProtos []string) (*tls.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	raw, err := Dial(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		p.recordTLS(host, port, "", tls.ConnectionState{}, err)
		return nil, err
	}
	var alpn []string
	for _, pr := range clientProtos {
		if pr == "h2" || pr == "http/1.1" {
			alpn = append(alpn, pr)
		}
	}
	uc := tls.Client(raw, &tls.Config{
		ServerName: host,
		RootCAs:    rootPool(), // never nil: system roots are deliberately not used
		NextProtos: alpn,
		MinVersion: tls.VersionTLS12,
	})
	err = uc.HandshakeContext(ctx)
	p.recordTLS(host, port, raw.RemoteAddr().String(), uc.ConnectionState(), err)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return uc, nil
}

// serveErrorOnce answers the first request on an intercepted connection with an error page.
func serveErrorOnce(c net.Conn, host string, cause error) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(req.Body, 1<<20))
	rw := &rawResponse{header: http.Header{}}
	writeErrorPage(rw, host, cause)
	rw.header.Set("Connection", "close")
	resp := &http.Response{
		StatusCode:    rw.status,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rw.header,
		Body:          io.NopCloser(strings.NewReader(rw.body.String())),
		ContentLength: int64(rw.body.Len()),
		Close:         true,
	}
	resp.Write(c)
}

type rawResponse struct {
	header http.Header
	status int
	body   strings.Builder
}

func (r *rawResponse) Header() http.Header         { return r.header }
func (r *rawResponse) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *rawResponse) WriteHeader(s int)           { r.status = s }

func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	// give the other direction a moment to drain after a half-close
	deadline := time.Now().Add(5 * time.Second)
	a.SetDeadline(deadline)
	b.SetDeadline(deadline)
	<-done
	a.Close()
	b.Close()
}
