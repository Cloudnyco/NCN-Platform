// grafana_proxy.go — embed Grafana in the console under /grafana, gated by an
// admin operator session. Grafana runs localhost-only on pop-03 and is reached
// through the ncn-grafana-tunnel (tyo 127.0.0.1:3001 → pop-03 :3000), so the
// only way in is this reverse proxy behind requireRole("admin"). Grafana itself
// is anonymous Viewer (read-only) + serve_from_sub_path=/grafana, so operators
// see dashboards with no second login. Path (incl the /grafana prefix) passes
// through unchanged. ReverseProxy handles the websocket upgrade Grafana Live uses.
package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

func newGrafanaProxy() http.HandlerFunc {
	target, _ := url.Parse("http://127.0.0.1:3001")
	rp := httputil.NewSingleHostReverseProxy(target)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		orig(r)
		r.Host = target.Host // Grafana checks Host against its root_url
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rp.ServeHTTP(w, r)
	}
}
