// This file is adapted from code in the net/http/httputil
// package of the Go standard library, which is by the
// Go Authors, and bears this copyright and license info:
//
//   Copyright 2011 The Go Authors. All rights reserved.
//   Use of this source code is governed by a BSD-style
//   license that can be found in the LICENSE file.
//
// This file has been modified from the standard lib to
// meet the needs of the application.

package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/inverse-inc/packetfence/go/caddy/caddy/caddyhttp/httpserver"
)

var (
	defaultDialer = &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	bufferPool = sync.Pool{New: createBuffer}
)

func createBuffer() interface{} {
	return make([]byte, 0, 32*1024)
}

func pooledIoCopy(dst io.Writer, src io.Reader) {
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	// CopyBuffer only uses buf up to its length and panics if it's 0.
	// Due to that we extend buf's length to its capacity here and
	// ensure it's always non-zero.
	bufCap := cap(buf)
	io.CopyBuffer(dst, src, buf[0:bufCap:bufCap])
}

// onExitFlushLoop is a callback set by tests to detect the state of the
// flushLoop() goroutine.
var onExitFlushLoop func()

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
type ReverseProxy struct {
	// Director must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	Director func(*http.Request)

	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// FlushInterval specifies the flush interval
	// to flush to the client while copying the
	// response body.
	// If zero, no periodic flushing is done.
	FlushInterval time.Duration
}

// Though the relevant directive prefix is just "unix:", url.Parse
// will - assuming the regular URL scheme - add additional slashes
// as if "unix" was a request protocol.
// What we need is just the path, so if "unix:/var/run/www.socket"
// was the proxy directive, the parsed hostName would be
// "unix:///var/run/www.socket", hence the ambiguous trimming.
func socketDial(hostName string) func(network, addr string) (conn net.Conn, err error) {
	return func(network, addr string) (conn net.Conn, err error) {
		return net.Dial("unix", hostName[len("unix://"):])
	}
}

// NewSingleHostReverseProxy returns a new ReverseProxy that rewrites
// URLs to the scheme, host, and base path provided in target. If the
// target's path is "/base" and the incoming request was for "/dir",
// the target request will be for /base/dir.
// Without logic: target's path is "/", incoming is "/api/messages",
// without is "/api", then the target request will be for /messages.
func NewSingleHostReverseProxy(target *url.URL, without string, keepalive int) *ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		if target.Scheme == "unix" {
			// to make Dial work with unix URL,
			// scheme and host have to be faked
			req.URL.Scheme = "http"
			req.URL.Host = "socket"
		} else {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
		}

		// We should remove the `without` prefix at first.
		if without != "" {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, without)
			if req.URL.Opaque != "" {
				req.URL.Opaque = strings.TrimPrefix(req.URL.Opaque, without)
			}
			if req.URL.RawPath != "" {
				req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, without)
			}
		}

		hadTrailingSlash := strings.HasSuffix(req.URL.Path, "/")
		req.URL.Path = path.Join(target.Path, req.URL.Path)
		// path.Join will strip off the last /, so put it back if it was there.
		if hadTrailingSlash && !strings.HasSuffix(req.URL.Path, "/") {
			req.URL.Path = req.URL.Path + "/"
		}

		// Trims the path of the socket from the URL path.
		// This is done because req.URL passed to your proxied service
		// will have the full path of the socket file prefixed to it.
		// Calling /test on a server that proxies requests to
		// unix:/var/run/www.socket will thus set the requested path
		// to /var/run/www.socket/test, rendering paths useless.
		if target.Scheme == "unix" {
			// See comment on socketDial for the trim
			socketPrefix := target.String()[len("unix://"):]
			req.URL.Path = strings.TrimPrefix(req.URL.Path, socketPrefix)
		}

		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
	}
	rp := &ReverseProxy{Director: director, FlushInterval: 250 * time.Millisecond} // flushing good for streaming & server-sent events
	if target.Scheme == "unix" {
		rp.Transport = &http.Transport{
			Dial: socketDial(target.String()),
		}
	} else if keepalive != http.DefaultMaxIdleConnsPerHost {
		// if keepalive is equal to the default,
		// just use default transport, to avoid creating
		// a brand new transport
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			Dial:                  defaultDialer.Dial,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		if keepalive == 0 {
			transport.DisableKeepAlives = true
		} else {
			transport.MaxIdleConnsPerHost = keepalive
		}
		if httpserver.HTTP2 {
			http2.ConfigureTransport(transport)
		}
		rp.Transport = transport
	}
	return rp
}

// UseInsecureTransport is used to facilitate HTTPS proxying
// when it is OK for upstream to be using a bad certificate,
// since this transport skips verification.
func (rp *ReverseProxy) UseInsecureTransport() {
	if rp.Transport == nil {
		transport := &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			Dial:                defaultDialer.Dial,
			TLSHandshakeTimeout: 10 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		}
		if httpserver.HTTP2 {
			http2.ConfigureTransport(transport)
		}
		rp.Transport = transport
	} else if transport, ok := rp.Transport.(*http.Transport); ok {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		// No http2.ConfigureTransport() here.
		// For now this is only added in places where
		// an http.Transport is actually created.
	}
}

// ServeHTTP serves the proxied request to the upstream by performing a roundtrip.
// It is designed to handle websocket connection upgrades as well.
func (rp *ReverseProxy) ServeHTTP(rw http.ResponseWriter, outreq *http.Request, respUpdateFn respUpdateFn) error {
	transport := rp.Transport
	if requestIsWebsocket(outreq) {
		transport = newConnHijackerTransport(transport)
	} else if transport == nil {
		transport = http.DefaultTransport
	}

	rp.Director(outreq)

	// Original incoming server request may be canceled by the
	// user or by std lib(e.g. too many idle connections).
	// Now we issue the new outgoing client request which
	// doesn't depend on the original one. (issue 1345)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outreq = outreq.WithContext(ctx)

	res, err := transport.RoundTrip(outreq)
	if err != nil {
		return err
	}

	isWebsocket := res.StatusCode == http.StatusSwitchingProtocols && strings.ToLower(res.Header.Get("Upgrade")) == "websocket"

	// Remove hop-by-hop headers listed in the
	// "Connection" header of the response.
	if c := res.Header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				res.Header.Del(f)
			}
		}
	}

	for _, h := range hopHeaders {
		res.Header.Del(h)
	}

	if respUpdateFn != nil {
		respUpdateFn(res)
	}

	if isWebsocket {
		res.Body.Close()
		hj, ok := rw.(http.Hijacker)
		if !ok {
			panic(httpserver.NonHijackerError{Underlying: rw})
		}

		conn, brw, err := hj.Hijack()
		if err != nil {
			return err
		}
		defer conn.Close()

		var backendConn net.Conn
		if hj, ok := transport.(*connHijackerTransport); ok {
			backendConn = hj.Conn
			if _, err := conn.Write(hj.Replay); err != nil {
				return err
			}
			bufferPool.Put(hj.Replay)
		} else {
			backendConn, err = net.Dial("tcp", outreq.URL.Host)
			if err != nil {
				return err
			}
			outreq.Write(backendConn)
		}
		defer backendConn.Close()

		// Proxy backend -> frontend.
		go pooledIoCopy(conn, backendConn)

		// Proxy frontend -> backend.
		//
		// NOTE: Hijack() sometimes returns buffered up bytes in brw which
		// would be lost if we didn't read them out manually below.
		if brw != nil {
			if n := brw.Reader.Buffered(); n > 0 {
				rbuf, err := brw.Reader.Peek(n)
				if err != nil {
					return err
				}
				backendConn.Write(rbuf)
			}
		}
		pooledIoCopy(backendConn, conn)
	} else {
		copyHeader(rw.Header(), res.Header)

		// The "Trailer" header isn't included in the Transport's response,
		// at least for *http.Transport. Build it up from Trailer.
		if len(res.Trailer) > 0 {
			trailerKeys := make([]string, 0, len(res.Trailer))
			for k := range res.Trailer {
				trailerKeys = append(trailerKeys, k)
			}
			rw.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
		}

		rw.WriteHeader(res.StatusCode)
		if len(res.Trailer) > 0 {
			// Force chunking if we saw a response trailer.
			// This prevents net/http from calculating the length for short
			// bodies and adding a Content-Length.
			if fl, ok := rw.(http.Flusher); ok {
				fl.Flush()
			}
		}
		rp.copyResponse(rw, res.Body)
		res.Body.Close() // close now, instead of defer, to populate res.Trailer
		copyHeader(rw.Header(), res.Trailer)
	}

	return nil
}

func (rp *ReverseProxy) copyResponse(dst io.Writer, src io.Reader) {
	if rp.FlushInterval != 0 {
		if wf, ok := dst.(writeFlusher); ok {
			mlw := &maxLatencyWriter{
				dst:     wf,
				latency: rp.FlushInterval,
				done:    make(chan bool),
			}
			go mlw.flushLoop()
			defer mlw.stop()
			dst = mlw
		}
	}
	pooledIoCopy(dst, src)
}

// skip these headers if they already exist.
// see https://github.com/inverse-inc/packetfence/go/caddy/caddy/pull/1112#discussion_r80092582
var skipHeaders = map[string]struct{}{
	"Content-Type":        {},
	"Content-Disposition": {},
	"Accept-Ranges":       {},
	"Set-Cookie":          {},
	"Cache-Control":       {},
	"Expires":             {},
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		if _, ok := dst[k]; ok {
			// skip some predefined headers
			// see https://github.com/inverse-inc/packetfence/go/caddy/caddy/issues/1086
			if _, shouldSkip := skipHeaders[k]; shouldSkip {
				continue
			}
			// otherwise, overwrite
			dst.Del(k)
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Alt-Svc",
	"Alternate-Protocol",
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Te",               // canonicalized version of "TE"
	"Trailer",          // not Trailers per URL above; http://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

type respUpdateFn func(resp *http.Response)

type hijackedConn struct {
	net.Conn
	hj *connHijackerTransport
}

func (c *hijackedConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	c.hj.Replay = append(c.hj.Replay, b[:n]...)
	return
}

func (c *hijackedConn) Close() error {
	return nil
}

type connHijackerTransport struct {
	*http.Transport
	Conn   net.Conn
	Replay []byte
}

func newConnHijackerTransport(base http.RoundTripper) *connHijackerTransport {
	t := &http.Transport{
		MaxIdleConnsPerHost: -1,
	}
	if b, _ := base.(*http.Transport); b != nil {
		tlsClientConfig := b.TLSClientConfig
		if tlsClientConfig.NextProtos != nil {
			tlsClientConfig = cloneTLSClientConfig(tlsClientConfig)
			tlsClientConfig.NextProtos = nil
		}

		t.Proxy = b.Proxy
		t.TLSClientConfig = tlsClientConfig
		t.TLSHandshakeTimeout = b.TLSHandshakeTimeout
		t.Dial = b.Dial
		t.DialTLS = b.DialTLS
	} else {
		t.Proxy = http.ProxyFromEnvironment
		t.TLSHandshakeTimeout = 10 * time.Second
	}
	hj := &connHijackerTransport{t, nil, bufferPool.Get().([]byte)[:0]}

	dial := getTransportDial(t)
	dialTLS := getTransportDialTLS(t)
	t.Dial = func(network, addr string) (net.Conn, error) {
		c, err := dial(network, addr)
		hj.Conn = c
		return &hijackedConn{c, hj}, err
	}
	t.DialTLS = func(network, addr string) (net.Conn, error) {
		c, err := dialTLS(network, addr)
		hj.Conn = c
		return &hijackedConn{c, hj}, err
	}

	return hj
}

// getTransportDial always returns a plain Dialer
// and defaults to the existing t.Dial.
func getTransportDial(t *http.Transport) func(network, addr string) (net.Conn, error) {
	if t.Dial != nil {
		return t.Dial
	}
	return defaultDialer.Dial
}

// getTransportDial always returns a TLS Dialer
// and defaults to the existing t.DialTLS.
func getTransportDialTLS(t *http.Transport) func(network, addr string) (net.Conn, error) {
	if t.DialTLS != nil {
		return t.DialTLS
	}

	// newConnHijackerTransport will modify t.Dial after calling this method
	// => Create a backup reference.
	plainDial := getTransportDial(t)

	// The following DialTLS implementation stems from the Go stdlib and
	// is identical to what happens if DialTLS is not provided.
	// Source: https://github.com/golang/go/blob/230a376b5a67f0e9341e1fa47e670ff762213c83/src/net/http/transport.go#L1018-L1051
	return func(network, addr string) (net.Conn, error) {
		plainConn, err := plainDial(network, addr)
		if err != nil {
			return nil, err
		}

		tlsClientConfig := t.TLSClientConfig
		if tlsClientConfig == nil {
			tlsClientConfig = &tls.Config{}
		}
		if !tlsClientConfig.InsecureSkipVerify && tlsClientConfig.ServerName == "" {
			tlsClientConfig.ServerName = stripPort(addr)
		}

		tlsConn := tls.Client(plainConn, tlsClientConfig)
		errc := make(chan error, 2)
		var timer *time.Timer
		if d := t.TLSHandshakeTimeout; d != 0 {
			timer = time.AfterFunc(d, func() {
				errc <- tlsHandshakeTimeoutError{}
			})
		}
		go func() {
			err := tlsConn.Handshake()
			if timer != nil {
				timer.Stop()
			}
			errc <- err
		}()
		if err := <-errc; err != nil {
			plainConn.Close()
			return nil, err
		}
		if !tlsClientConfig.InsecureSkipVerify {
			hostname := tlsClientConfig.ServerName
			if hostname == "" {
				hostname = stripPort(addr)
			}
			if err := tlsConn.VerifyHostname(hostname); err != nil {
				plainConn.Close()
				return nil, err
			}
		}

		return tlsConn, nil
	}
}

// stripPort returns address without its port if it has one and
// works with IP addresses as well as hostnames formatted as host:port.
//
// IPv6 addresses (excluding the port) must be enclosed in
// square brackets similar to the requirements of Go's stdlib.
func stripPort(address string) string {
	// Keep in mind that the address might be a IPv6 address
	// and thus contain a colon, but not have a port.
	portIdx := strings.LastIndex(address, ":")
	ipv6Idx := strings.LastIndex(address, "]")
	if portIdx > ipv6Idx {
		address = address[:portIdx]
	}
	return address
}

type tlsHandshakeTimeoutError struct{}

func (tlsHandshakeTimeoutError) Timeout() bool   { return true }
func (tlsHandshakeTimeoutError) Temporary() bool { return true }
func (tlsHandshakeTimeoutError) Error() string   { return "net/http: TLS handshake timeout" }

// cloneTLSClientConfig is like cloneTLSConfig but omits
// the fields SessionTicketsDisabled and SessionTicketKey.
// This makes it safe to call cloneTLSClientConfig on a config
// in active use by a server.
func cloneTLSClientConfig(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return &tls.Config{}
	}
	return &tls.Config{
		Rand:                        cfg.Rand,
		Time:                        cfg.Time,
		Certificates:                cfg.Certificates,
		NameToCertificate:           cfg.NameToCertificate,
		GetCertificate:              cfg.GetCertificate,
		RootCAs:                     cfg.RootCAs,
		NextProtos:                  cfg.NextProtos,
		ServerName:                  cfg.ServerName,
		ClientAuth:                  cfg.ClientAuth,
		ClientCAs:                   cfg.ClientCAs,
		InsecureSkipVerify:          cfg.InsecureSkipVerify,
		CipherSuites:                cfg.CipherSuites,
		PreferServerCipherSuites:    cfg.PreferServerCipherSuites,
		ClientSessionCache:          cfg.ClientSessionCache,
		MinVersion:                  cfg.MinVersion,
		MaxVersion:                  cfg.MaxVersion,
		CurvePreferences:            cfg.CurvePreferences,
		DynamicRecordSizingDisabled: cfg.DynamicRecordSizingDisabled,
		Renegotiation:               cfg.Renegotiation,
	}
}

func requestIsWebsocket(req *http.Request) bool {
	return strings.ToLower(req.Header.Get("Upgrade")) == "websocket" && strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency time.Duration

	lk   sync.Mutex // protects Write + Flush
	done chan bool
}

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.lk.Lock()
	defer m.lk.Unlock()
	return m.dst.Write(p)
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(m.latency)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			if onExitFlushLoop != nil {
				onExitFlushLoop()
			}
			return
		case <-t.C:
			m.lk.Lock()
			m.dst.Flush()
			m.lk.Unlock()
		}
	}
}

func (m *maxLatencyWriter) stop() { m.done <- true }
