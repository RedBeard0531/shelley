// Package mcp connects Shelley to Streamable HTTP MCP servers. The Shelley
// server owns the sessions; `shelley mcp` and the UI reach them through its
// HTTP API.
package mcp

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// Server is a registered MCP server.
type Server struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Description string            `json:"description"`
	Headers     map[string]string `json:"headers"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Names can't contain dots, so that SERVER.TOOL splits at the first one.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Validate reports whether s is a usable configuration.
func (s Server) Validate() error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("invalid name %q: use letters, digits, _ and -, starting with a letter or digit", s.Name)
	}
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid URL %q: must be http or https", s.URL)
	}
	seen := map[string]bool{}
	for k, v := range s.Headers {
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) {
			return fmt.Errorf("invalid header %q", k)
		}
		if seen[http.CanonicalHeaderKey(k)] {
			return fmt.Errorf("duplicate header %q", k)
		}
		seen[http.CanonicalHeaderKey(k)] = true
	}
	return nil
}

// OAuthCapable reports whether s can log in with OAuth: it has no static
// Authorization header.
func (s Server) OAuthCapable() bool {
	for k := range s.Headers {
		if strings.EqualFold(k, "Authorization") {
			return false
		}
	}
	return true
}

// sameConnection reports whether sessions with s and o are interchangeable.
func (s Server) sameConnection(o Server) bool {
	return s.URL == o.URL && maps.Equal(s.Headers, o.Headers)
}
