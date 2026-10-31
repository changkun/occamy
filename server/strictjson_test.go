// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type strictInner struct {
	Name string `json:"name"`
}

type strictEmbedded struct {
	Kind string `json:"kind"`
}

// strictCustom decodes itself, from members that are none of its fields.
type strictCustom struct{ v string }

func (c *strictCustom) UnmarshalJSON(b []byte) error {
	c.v = string(b)
	return nil
}

type strictRequest struct {
	strictEmbedded
	ID       string                 `json:"id"`
	Count    int                    `json:"count,omitempty"`
	Untagged string                 // encoding/json names it Untagged
	Skipped  string                 `json:"-"`
	Inner    strictInner            `json:"inner"`
	Ptr      *strictInner           `json:"ptr"`
	List     []strictInner          `json:"list"`
	Matrix   [][]strictInner        `json:"matrix"`
	Labels   map[string]string      `json:"labels"`
	ByName   map[string]strictInner `json:"by_name"`
	Raw      json.RawMessage        `json:"raw"`
	When     time.Time              `json:"when"`
	Custom   strictCustom           `json:"custom"`
	Any      any                    `json:"any"`
	hidden   string
}

func TestCheckMembers(t *testing.T) {
	_ = strictRequest{}.hidden
	_ = strictRequest{}.Custom.v
	typ := reflect.TypeFor[*strictRequest]()
	for _, tt := range []struct {
		body string
		want string // in the error, or "" for none
	}{
		{`{"id":"a","count":1,"Untagged":"u","kind":"k"}`, ""},
		{`{"inner":{"name":"x"},"ptr":{"name":"y"},"list":[{"name":"z"}],"matrix":[[{"name":"w"}]]}`, ""},
		{`{"labels":{"a":"1","A":"2"},"raw":{"x":1,"X":2},"when":"2026-10-31T00:00:00Z","any":{"q":1}}`, ""},
		{`{"ptr":null,"list":null,"labels":null}`, ""},
		{`{"by_name":{"a":{"name":"x"},"A":{"name":"y"}},"custom":{"Anything":1,"anything":2}}`, ""},
		// Exact duplicates, at every level.
		{`{"id":"a","id":"b"}`, `field "id" appears more than once`},
		{`{"inner":{"name":"x","name":"y"}}`, `field "name" appears more than once`},
		{`{"list":[{"name":"x"},{"name":"y","name":"z"}]}`, `field "name" appears more than once`},
		{`{"matrix":[[{"name":"x","name":"y"}]]}`, `field "name" appears more than once`},
		{`{"labels":{"a":"1","a":"2"}}`, `field "a" appears more than once`},
		{`{"by_name":{"a":{"name":"x","name":"y"}}}`, `field "name" appears more than once`},
		{`{"custom":{"x":1,"x":2}}`, `field "x" appears more than once`},
		{`{"raw":{"x":1,"x":2}}`, `field "x" appears more than once`},
		{`{"any":{"q":1,"q":2}}`, `field "q" appears more than once`},
		{`{"kind":"a","kind":"b"}`, `field "kind" appears more than once`},
		// Names in another case, alone or after the field's own.
		{`{"ID":"a"}`, `unknown field "ID": field names are case-sensitive, did you mean "id"?`},
		{`{"id":"a","Id":"b"}`, `unknown field "Id"`},
		{`{"inner":{"Name":"x"}}`, `unknown field "Name"`},
		{`{"ptr":{"NAME":"x"}}`, `unknown field "NAME"`},
		{`{"list":[{"name":"x","nAme":"y"}]}`, `unknown field "nAme"`},
		{`{"by_name":{"a":{"Name":"x"}}}`, `unknown field "Name"`},
		{`{"untagged":"u"}`, `unknown field "untagged"`},
		{`{"Kind":"k"}`, `unknown field "Kind"`},
		// encoding/json folds the Kelvin sign and the long s into k and s.
		{"{\"Kind\":\"k\"}", `unknown field`},
		{"{\"id\":\"a\",\"ſkipped\":\"s\"}", `unknown field`},
		// Unknown fields, the skipped one, and an unexported one.
		{`{"Skipped":"s"}`, `unknown field "Skipped"`},
		{`{"-":"s"}`, `unknown field "-"`},
		{`{"hidden":"h"}`, `unknown field "hidden"`},
		{`{"strictEmbedded":{}}`, `unknown field "strictEmbedded"`},
		// Too deep.
		{`{"list":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}`, "nests deeper than"},
		// Syntax errors are the decoder's to report.
		{`{"id":`, ""},
		{`{"id" "a"}`, ""},
		{`{"id":"a"`, ""},
		{``, ""},
		// Only the first value is checked; the decoder refuses a second.
		{`{"id":"a"} {"id":"b","id":"c"}`, ""},
		// Values that are not objects are the decoder's to refuse.
		{`["id","id"]`, ""},
		{`"id"`, ""},
	} {
		err := checkMembers([]byte(tt.body), typ)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v, want none", tt.body, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: %v, want an error with %q", tt.body, err, tt.want)
		}
	}
}

// TestStrictRequestBodies sends API v1's routes bodies with a field twice,
// or in another case, which encoding/json alone would take, the last
// duplicate winning, and checks that each is refused before it is used.
func TestStrictRequestBodies(t *testing.T) {
	ts, fb := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	jsonType := http.Header{"Content-Type": {"application/json"}}
	for _, tt := range []struct {
		name, path, token, body string
	}{
		{"sign-in, username twice", "/api/v1/auth/token", "", `{"username":"alice","password":"` + bobPassword + `","username":"bob"}`},
		{"sign-in, username in another case", "/api/v1/auth/token", "", `{"username":"bob","password":"` + bobPassword + `","Username":"alice"}`},
		{"sign-in, only in another case", "/api/v1/auth/token", "", `{"USERNAME":"bob","password":"` + bobPassword + `"}`},
		{"session, connection twice", "/api/v1/sessions", alice, `{"connection":"dev-shell","connection":"ops-shell"}`},
		{"session, connection in another case", "/api/v1/sessions", alice, `{"connection":"dev-shell","Connection":"ops-shell"}`},
		{"session, host keys twice", "/api/v1/sessions", alice, `{"protocol":"ssh","target":"10.0.0.1","host_keys":["a"],"host_keys":["b"]}`},
	} {
		h := jsonType.Clone()
		if tt.token != "" {
			h = bearer(tt.token)
		}
		r := do(t, http.MethodPost, ts.srv.URL+tt.path, h, tt.body)
		if r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tt.name, r.code, r.body)
			continue
		}
		if e := decodeError(t, r.body); e.Code != codeBadRequest {
			t.Errorf("%s: %+v", tt.name, e)
		}
	}
	if tg, _, _ := fb.last(); tg.connection != "" {
		t.Errorf("a refused body opened connection %q", tg.connection)
	}

	// A share's role twice.
	o := ts.openSession(t, alice, "ops-shell")
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"read-only","role":"viewer"}`); r.code != http.StatusBadRequest {
		t.Errorf("a share's role twice: %d %s", r.code, r.body)
	}
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"Role":"viewer"}`); r.code != http.StatusBadRequest {
		t.Errorf("a share's role in another case: %d %s", r.code, r.body)
	}
	// The OIDC code exchange: a code that does not exist answers 401, a
	// body with a field twice 400 before any code is looked up.
	ot := newOIDCTest(t, nil)
	for body, want := range map[string]int{
		`{"code":"x","code_verifier":"` + strings.Repeat("a", 43) + `"}`:                                                   http.StatusUnauthorized,
		`{"code":"x","code_verifier":"` + strings.Repeat("a", 43) + `","code_verifier":"` + strings.Repeat("b", 43) + `"}`: http.StatusBadRequest,
		`{"code":"x","Code_verifier":"` + strings.Repeat("a", 43) + `"}`:                                                   http.StatusBadRequest,
	} {
		if r := do(t, http.MethodPost, ot.url+"/api/v1/auth/oidc/token", jsonType, body); r.code != want {
			t.Errorf("OIDC token %s: %d %s, want %d", body, r.code, r.body, want)
		}
	}
}
