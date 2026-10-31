// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

// apiDoc is the reference of the routes, relative to this package.
var apiDoc = filepath.Join("..", "docs", "api.md")

// routeHeading matches a Markdown heading that names a route in a code
// span, such as ### `GET /api/v1/connections`, at any level, so that a
// route documented at the wrong level is reported rather than missed.
var routeHeading = regexp.MustCompile("^(#{1,6}) `([A-Z]+ /[^`]*)`\\s*$")

// TestAPIDoc checks that docs/api.md has exactly one section, headed
// ### `METHOD /path` with the pattern as registered, for each route of
// the public listener, with every optional route on, and of the debug
// listener; and that each such heading names one of those routes.
func TestAPIDoc(t *testing.T) {
	routes := registeredRoutes(t)
	documented, problems := documentedRoutes(t, apiDoc)
	for _, p := range problems {
		t.Error(p)
	}

	var missing, twice, unknown []string
	for _, r := range routes {
		switch documented[r] {
		case 0:
			missing = append(missing, "### `"+r+"`")
		case 1:
		default:
			twice = append(twice, "### `"+r+"`")
		}
	}
	for r := range documented {
		if !slices.Contains(routes, r) {
			unknown = append(unknown, "### `"+r+"`")
		}
	}
	slices.Sort(unknown)
	if len(missing) > 0 {
		t.Errorf("%s has no section for these routes; add one headed as shown:\n\t%s", apiDoc, strings.Join(missing, "\n\t"))
	}
	if len(twice) > 0 {
		t.Errorf("%s has more than one section for these routes:\n\t%s", apiDoc, strings.Join(twice, "\n\t"))
	}
	if len(unknown) > 0 {
		t.Errorf("%s has sections for routes that occamyd does not register on either listener:\n\t%s", apiDoc, strings.Join(unknown, "\n\t"))
	}
}

// registeredRoutes returns the patterns of the public listener's routes,
// with auth.oidc and legacy_v0 on, and of the debug listener's. newServer
// does not contact the OIDC provider, so its address need not answer.
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	captureLog(t) // legacy_v0's warning
	f := testFile(t)
	f.LegacyV0 = true
	f.Auth.OIDC = &config.OIDC{
		Issuer:       "http://127.0.0.1:1",
		ClientID:     "occamy",
		RedirectURL:  "http://127.0.0.1:1/api/v1/auth/oidc/callback",
		ReturnURLs:   []string{"http://127.0.0.1:1/"},
		Scopes:       config.DefaultOIDCScopes,
		SubjectClaim: "sub",
	}
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.shutdown)
	if s.oidc == nil || s.v0 == nil {
		t.Fatalf("a server without its optional routes: oidc %v, legacy_v0 %v", s.oidc != nil, s.v0 != nil)
	}
	routes := slices.Concat(s.mux().patterns(), debugMux().patterns())
	if len(routes) == 0 {
		t.Fatal("no routes registered")
	}
	return routes
}

// documentedRoutes returns how many headings of the file at path name
// each route, outside fenced code blocks, and describes the route
// headings that are not at level 3.
func documentedRoutes(t *testing.T, path string) (map[string]int, []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	routes := map[string]int{}
	var problems []string
	var fence string // the fence of the code block the scan is in, if any
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimLeft(line, " ")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		m := routeHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != "###" {
			problems = append(problems, fmt.Sprintf("%s:%d: route %s is under a level %d heading; route sections are ### `METHOD /path`", path, n, m[2], len(m[1])))
		}
		routes[m[2]]++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return routes, problems
}
