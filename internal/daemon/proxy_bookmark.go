package daemon

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// proxyBookmark reverse-proxies a request to a bookmark route's ExternalURL.
// The upstream Host header is set to the external URL's host so TLS SNI and
// virtual-hosted origins work correctly. Same-origin 3xx Location headers
// and Set-Cookie Domain attributes from the upstream are rewritten so the
// browser keeps the .vibe host in the URL bar and actually stores cookies.
//
// The bookmark's path is treated as a "landing destination" rather than a
// proxy prefix: a request to name.vibe/ is 302'd to the path; every other
// request proxies to the origin with its path unchanged. This lets a
// bookmark like https://host/app/dashboard work correctly for apps whose
// static assets live at the origin root (modern SPAs, Home Assistant, etc.)
// — prepending /app/dashboard to every asset request would break them.
func (s *Server) proxyBookmark(w http.ResponseWriter, r *http.Request, route *Route, vibeHost string) {
	raw, err := url.Parse(route.ExternalURL)
	if err != nil {
		http.Error(w, "invalid upstream url", http.StatusBadGateway)
		return
	}

	// Root requests land on the bookmark's path.
	if (r.URL.Path == "" || r.URL.Path == "/") && raw.Path != "" && raw.Path != "/" {
		dest := raw.Path
		if raw.RawQuery != "" {
			dest += "?" + raw.RawQuery
		}
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}

	target := &url.URL{Scheme: raw.Scheme, Host: raw.Host}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		// Default Go director leaves req.Host untouched (browser-supplied
		// name.vibe). Force the upstream's host so SNI and vhost routing
		// see the right name.
		req.Host = target.Host
		// Setting the X-Forwarded-For slot to nil signals ReverseProxy to
		// omit the header entirely (see net/http/httputil.ServeHTTP). Strict
		// upstreams (e.g. Home Assistant) return HTTP 400 when they see an
		// X-Forwarded-For from an untrusted proxy; this bookmark-proxy is a
		// transparent URL mask, not a load balancer, so X-Forwarded-* should
		// not appear. Nil-assignment also scrubs any client-supplied value.
		req.Header["X-Forwarded-For"] = nil
		// Rewrite Origin/Referer to the upstream so same-origin auth checks
		// pass. The browser sends these as the .vibe host, but upstreams
		// that whitelist their own host (e.g. Jekyll Admin's 403 on foreign
		// Origins) would reject them. Since we already force req.Host to the
		// upstream, mirroring Origin/Referer keeps the request same-origin
		// from the upstream's point of view.
		rewriteOriginHeader(req, target)
		rewriteRefererHeader(req, target)
	}

	proxy.Transport = buildUpstreamTransport(target.Scheme == "https" && route.InsecureSkipVerify)

	vibeScheme := s.vibeScheme()
	proxy.ModifyResponse = func(resp *http.Response) error {
		rewriteLocationToVibe(resp, target, vibeHost, vibeScheme)
		stripCookieDomain(resp)
		return nil
	}

	route.TouchActivity()
	proxy.ServeHTTP(w, r)
}

// buildUpstreamTransport returns an http.Transport suited for reverse-proxy
// upstreams. Mirrors http.DefaultTransport's dial/timeout defaults; flips
// InsecureSkipVerify on per the route's opt-in for self-signed targets.
func buildUpstreamTransport(insecure bool) *http.Transport {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return tr
}

// rewriteLocationToVibe rewrites a 3xx Location header pointing at the
// upstream origin so the browser stays on the .vibe host. Relative
// Locations are left alone — the browser resolves them against the current
// URL, which is already the .vibe host.
func rewriteLocationToVibe(resp *http.Response, target *url.URL, vibeHost, vibeScheme string) {
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil {
		return
	}
	if !u.IsAbs() {
		return
	}
	if !strings.EqualFold(u.Host, target.Host) || !strings.EqualFold(u.Scheme, target.Scheme) {
		return
	}
	u.Scheme = vibeScheme
	u.Host = vibeHost
	resp.Header.Set("Location", u.String())
}

// stripCookieDomain removes any Domain attribute from Set-Cookie headers so
// cookies bind to the .vibe host the browser actually sees. Without this,
// upstream-issued cookies with Domain=upstream.example.com get dropped by
// the browser and logins break under proxy mode.
func stripCookieDomain(resp *http.Response) {
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}
	cleaned := make([]string, len(cookies))
	for i, c := range cookies {
		cleaned[i] = removeDomainAttr(c)
	}
	resp.Header.Del("Set-Cookie")
	for _, c := range cleaned {
		resp.Header.Add("Set-Cookie", c)
	}
}

// rewriteOriginHeader replaces the Origin header (if present) so its scheme
// and host match the upstream. Leaves other fields alone.
func rewriteOriginHeader(req *http.Request, target *url.URL) {
	orig := req.Header.Get("Origin")
	if orig == "" {
		return
	}
	u, err := url.Parse(orig)
	if err != nil {
		return
	}
	u.Scheme = target.Scheme
	u.Host = target.Host
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
}

// rewriteRefererHeader replaces the scheme/host in Referer with the upstream's
// so the upstream sees a same-origin Referer. Path/query are preserved so
// upstream analytics/debug still show where the request came from.
func rewriteRefererHeader(req *http.Request, target *url.URL) {
	ref := req.Header.Get("Referer")
	if ref == "" {
		return
	}
	u, err := url.Parse(ref)
	if err != nil {
		return
	}
	u.Scheme = target.Scheme
	u.Host = target.Host
	req.Header.Set("Referer", u.String())
}

func removeDomainAttr(cookie string) string {
	parts := strings.Split(cookie, ";")
	out := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "domain=") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}
