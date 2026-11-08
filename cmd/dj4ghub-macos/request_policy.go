package main

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// A browser must address the configured listener directly and use the same
// origin. This also rejects hostile DNS names resolved to a loopback address.
// Native loopback clients without browser headers retain their existing API.
func localRequestPolicy(listen string, next http.Handler) http.Handler {
	bindHost, bindPort, bindErr := net.SplitHostPort(listen)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || bindErr != nil || port != bindPort {
			writeError(w, http.StatusForbidden, "访问地址不匹配")
			return
		}
		allowed := strings.EqualFold(host, bindHost)
		ip := net.ParseIP(bindHost)
		if ip != nil && ip.IsLoopback() {
			allowed = strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
		} else if bindHost == "" || bindHost == "0.0.0.0" || bindHost == "::" {
			allowed = net.ParseIP(host) != nil
		}
		if !allowed || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "不允许此来源访问")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, r.Host) ||
				parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				writeError(w, http.StatusForbidden, "不允许跨来源访问")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
