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
	if !s.previewAllowed(r, pv) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "preview requires a token or API key")
		return
	}
	prefix := "/p/" + sandboxID + "/" + strconv.Itoa(port)
	s.servePreview(w, r, pv, prefix, false)
}

// routePreviewHost serves <port>-sbx-<id>.<preview_domain> before the API mux.
func (s *Server) routePreviewHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domain := strings.Trim(strings.ToLower(strings.TrimSpace(s.mgr.Config().Network.PreviewDomain)), ".")
		if domain == "" {
			next.ServeHTTP(w, r)
			return
		}
		port, hexID, ok := parsePreviewHost(requestHost(r), domain)
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
		if !s.previewAllowed(r, pv) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "preview requires a token or API key")
			return
		}
		s.servePreview(w, r, pv, "", false)
	})
}

func (s *Server) previewAllowed(r *http.Request, pv core.Preview) bool {
	if pv.Public && s.mgr.Config().Network.AllowPublicPreviews {
		return true
	}
	token := r.Header.Get("X-Sbx-Preview-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if s.mgr.PreviewTokenOK(pv, token) {
		return true
	}
	key, err := s.authenticate(r)
	if err != nil || !key.HasScope("sandbox") {
		return false
	}
	_, err = s.mgr.Get(r.Context(), core.Actor{KeyID: key.ID, Admin: key.Admin()}, pv.SandboxID)
	return err == nil
}

func (s *Server) servePreview(w http.ResponseWriter, r *http.Request, pv core.Preview, prefix string, ownerOnly bool) {
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
	out.Header.Del("Cookie")
	out.Header.Del("X-Sbx-Preview-Token")
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

func requestHost(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
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
