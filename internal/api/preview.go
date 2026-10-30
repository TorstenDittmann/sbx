package api

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/core"
)

func (s *Server) proxyPublicPath(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("sandboxId")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(sandboxID, "sbx_") {
		writeErr(w, http.StatusNotFound, "not_found", "preview not found")
		return
	}
	pv, ok := s.mgr.PreviewByPort(sandboxID, port)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "preview not found")
		return
	}
	if refuseKeyInPreviewURL(w, r) {
		return
	}
	prefix := "/p/" + sandboxID + "/" + strconv.Itoa(port)
	ok, fromQuery := s.previewAllowed(r, pv)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "preview requires a token or API key")
		return
	}
	if fromQuery != "" && s.exchangePreviewToken(w, r, fromQuery, prefix+"/") {
		return
	}
	// Path previews of every sandbox share the server origin, so a cookie
	// one sandbox set could be read by another. Only host previews, which
	// each get their own origin, forward the browser's cookies.
	s.servePreview(w, r, pv, prefix, false, false)
}

// routePreviewHost serves <port>-sbx-<id>.<preview_domain> before the API mux.
func (s *Server) routePreviewHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domain := strings.Trim(strings.ToLower(strings.TrimSpace(s.mgr.Config().Network.PreviewDomain)), ".")
		if domain == "" {
			next.ServeHTTP(w, r)
			return
		}
		port, hexID, ok := parsePreviewHost(s.requestHost(r), domain)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		sandboxID := "sbx_" + hexID
		pv, found := s.mgr.PreviewByPort(sandboxID, port)
		if !found {
			writeErr(w, http.StatusNotFound, "not_found", "preview not found")
			return
		}
		if refuseKeyInPreviewURL(w, r) {
			return
		}
		ok, fromQuery := s.previewAllowed(r, pv)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "preview requires a token or API key")
			return
		}
		if fromQuery != "" && s.exchangePreviewToken(w, r, fromQuery, "/") {
			return
		}
		s.servePreview(w, r, pv, "", false, true)
	})
}

// previewCookie carries a preview token after the first ?token= request, so
// the page's own subresources and XHRs are authorized too.
const previewCookie = "sbx_preview"

// previewAllowed reports whether r may open pv. fromQuery is the token when
// it was accepted from the ?token= query parameter, so the caller can swap
// it for a cookie.
func (s *Server) previewAllowed(r *http.Request, pv core.Preview) (ok bool, fromQuery string) {
	if pv.Public && s.mgr.Config().Network.AllowPublicPreviews {
		return true, ""
	}
	if s.mgr.PreviewTokenOK(pv, r.Header.Get("X-Sbx-Preview-Token")) {
		return true, ""
	}
	if token := r.URL.Query().Get("token"); s.mgr.PreviewTokenOK(pv, token) {
		return true, token
	}
	// A sandbox app could set a same-named cookie for another path, so
	// every value the browser sent is tried.
	for _, c := range r.Cookies() {
		if c.Name == previewCookie && s.mgr.PreviewTokenOK(pv, c.Value) {
			return true, ""
		}
	}
	key, err := s.authenticate(r)
	if err != nil || !key.HasScope("sandbox") {
		return false, ""
	}
	_, err = s.mgr.Get(r.Context(), core.Actor{KeyID: key.ID, Admin: key.Admin()}, pv.SandboxID)
	return err == nil, ""
}

// exchangePreviewToken stores token in an HttpOnly cookie scoped to path. A
// GET or HEAD is redirected to the same URL without ?token= so the secret
// leaves the address bar and history; it reports true when it redirected.
func (s *Server) exchangePreviewToken(w http.ResponseWriter, r *http.Request, token, path string) bool {
	http.SetCookie(w, &http.Cookie{
		Name: previewCookie, Value: token, Path: path,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || strings.HasPrefix(s.externalBase(r), "https://"),
	})
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if headerHasToken(r.Header, "Connection", "upgrade") {
		return false
	}
	u := *r.URL
	q := u.Query()
	q.Del("token")
	u.RawQuery = q.Encode()
	u.Scheme, u.Host = "", ""
	http.Redirect(w, r, u.RequestURI(), http.StatusFound)
	return true
}

// filterCookies drops the preview token cookie from a Cookie header value.
func filterCookies(header string) string {
	var keep []string
	for _, part := range strings.Split(header, ";") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.TrimSpace(part) == "" || name == previewCookie {
			continue
		}
		keep = append(keep, strings.TrimSpace(part))
	}
	return strings.Join(keep, "; ")
}

func (s *Server) servePreview(w http.ResponseWriter, r *http.Request, pv core.Preview, prefix string, ownerOnly, forwardCookies bool) {
	// A path preview runs sandbox code on the API and dashboard origin. A
	// service worker registered there could take over /ui/ and read every
	// key typed into it, so path previews may not register one.
	pathPreview := prefix != ""
	if pathPreview && r.Header.Get("Service-Worker") != "" {
		writeErr(w, http.StatusForbidden, "forbidden", "path previews cannot register a service worker; set network.preview_domain to serve previews on their own origin")
		return
	}
	if err := s.mgr.AcquirePreview(pv.SandboxID); err != nil {
		writeManager(w, err)
		return
	}
	defer s.mgr.ReleasePreview(pv.SandboxID)

	var conn io.ReadWriteCloser
	var err error
	if ownerOnly {
		conn, _, err = s.mgr.DialPreview(r.Context(), actor(r), pv.SandboxID, pv.ID)
	} else {
		conn, _, err = s.mgr.OpenPreview(r.Context(), pv.SandboxID, pv.ID)
	}
	if err != nil {
		writeManager(w, err)
		return
	}
	defer conn.Close()
	upstream := &throttleStream{ReadWriteCloser: conn, mgr: s.mgr, sandboxID: pv.SandboxID}

	rest := r.URL.Path
	if prefix != "" {
		rest = strings.TrimPrefix(r.URL.Path, prefix)
	}
	if rest == "" {
		rest = "/"
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = "http"
	out.URL.Host = fmt.Sprintf("127.0.0.1:%d", pv.Port)
	out.URL.Path = rest
	q := out.URL.Query()
	q.Del("access_token")
	q.Del("token")
	out.URL.RawQuery = q.Encode()
	out.Host = out.URL.Host
	out.Header.Del("Authorization")
	cookies := ""
	if forwardCookies {
		cookies = filterCookies(strings.Join(r.Header.Values("Cookie"), "; "))
	}
	out.Header.Del("Cookie")
	if cookies != "" {
		out.Header.Set("Cookie", cookies)
	}
	out.Header.Del("X-Sbx-Preview-Token")
	// The browser's Referer and Origin can carry ?access_token= or the API
	// origin; the sandbox needs neither.
	out.Header.Del("Referer")
	out.Header.Del("Origin")
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		stripHop(out.Header)
	}
	if err := out.Write(upstream); err != nil {
		writeErr(w, http.StatusBadGateway, "driver", "preview connection failed")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), out)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "driver", "preview response failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		s.spliceUpgrade(w, upstream, resp)
		return
	}
	stripHop(resp.Header)
	dropPreviewSetCookie(resp.Header)
	// Never let a sandbox widen a service worker's scope past its preview.
	resp.Header.Del("Service-Worker-Allowed")
	if pathPreview {
		// Path previews never get the browser's cookies back, so a cookie
		// set here could only reach the API origin and other sandboxes.
		resp.Header.Del("Set-Cookie")
	}
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

type throttleStream struct {
	io.ReadWriteCloser
	mgr       *core.Manager
	sandboxID string
}

func (t *throttleStream) Read(p []byte) (int, error) {
	n, err := t.ReadWriteCloser.Read(p)
	if n > 0 {
		t.mgr.ThrottlePreview(t.sandboxID, n)
	}
	return n, err
}

func (t *throttleStream) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.mgr.ThrottlePreview(t.sandboxID, len(p))
	}
	return t.ReadWriteCloser.Write(p)
}

// dropPreviewSetCookie removes upstream attempts to set the token cookie.
func dropPreviewSetCookie(h http.Header) {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, v := range values {
		name, _, _ := strings.Cut(strings.TrimSpace(v), "=")
		if name != previewCookie {
			h.Add("Set-Cookie", v)
		}
	}
}

// requestHost is the host the client asked for. X-Forwarded-Host is only
// honored when the operator says a proxy in front sets it; otherwise any
// client could pick which preview host a request is routed as.
func (s *Server) requestHost(r *http.Request) string {
	host := ""
	if s.mgr.Config().Network.TrustForwardedHost {
		host = r.Header.Get("X-Forwarded-Host")
	}
	if host == "" {
		host = r.Host
	}
	host = strings.TrimSpace(strings.Split(host, ",")[0])
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func parsePreviewHost(host, domain string) (int, string, bool) {
	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return 0, "", false
	}
	label := strings.TrimSuffix(host, suffix)
	portPart, hexID, ok := strings.Cut(label, "-sbx-")
	if !ok || len(hexID) != 16 {
		return 0, "", false
	}
	for _, c := range hexID {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return 0, "", false
		}
	}
	port, err := strconv.Atoi(portPart)
	if err != nil || port < 1 || port > 65535 {
		return 0, "", false
	}
	return port, hexID, true
}
