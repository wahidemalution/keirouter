package gateway

import (
	"net"
	"net/http"
)

const (
	portalDevSub   = "portal:dev-local"
	portalDevEmail = "dev@localhost"
)

// devLoginForwardingHeaders are set by reverse proxies and CDNs. A request that
// carries any of them did not come straight from a local client.
var devLoginForwardingHeaders = []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP", "Forwarded"}

// devLoginRequestAllowed reports whether r is a direct local request: loopback
// peer address, no proxy forwarding headers, and a loopback Host. Every check
// must pass, so a proxy that rewrites the peer to 127.0.0.1 still fails on the
// headers or the Host it forwards.
func devLoginRequestAllowed(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range devLoginForwardingHeaders {
		if len(r.Header.Values(h)) > 0 {
			return false
		}
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

// handlePortalDevLogin issues a portal session without Google or Turnstile.
// It is only routed when portal_sso.dev_login is on, and it answers 404 to any
// request that is not a direct local one, so it looks absent from outside.
func (s *Server) handlePortalDevLogin(w http.ResponseWriter, r *http.Request) {
	if !devLoginRequestAllowed(r) {
		http.NotFound(w, r)
		return
	}
	tok, err := s.auth.IssuePortalSession(portalDevSub, portalDevEmail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}
	s.setPortalCookie(w, r, tok)
	w.WriteHeader(http.StatusNoContent)
}
