package app

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// With the built-in office (COVE_OFFICE_INTERNAL), Collabora Online runs next to Cove and Cove passes its pages and
// live connection through, so documents open at Cove's own address. Collabora's admin pages aren't passed through.

var officePaths = []string{"/browser/", "/hosting/", "/cool/", "/lool/"}

func (s *Server) officeProxy() http.Handler {
	target, err := url.Parse(s.cfg.OfficeInternal)
	if err != nil {
		return http.NotFoundHandler()
	}
	public, _ := url.Parse(s.cfg.URL)
	p := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
			r.Out.Host = public.Host // Collabora writes links with the address the browser used
		},
		FlushInterval: -1,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/admin") || strings.HasPrefix(r.URL.Path, "/cool/getMetrics") {
			http.NotFound(w, r)
			return
		}
		p.ServeHTTP(w, r)
	})
}
