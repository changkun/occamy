// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/netip"
	"path/filepath"
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
		// Too deep: the object is the first level, so 31 arrays in it
		// are the most it takes.
		{`{"any":` + strings.Repeat("[", 31) + strings.Repeat("]", 31) + `}`, ""},
		{`{"any":` + strings.Repeat("[", 32) + strings.Repeat("]", 32) + `}`, "the body nests deeper than 32"},
		{`{"list":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}`, "the body nests deeper than 32"},
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

// requestTypes are the types that the routes decode request bodies
// into, by name. TestRequestTypesFlat fails for a decodeJSON call whose
// type is not listed.
var requestTypes = map[string]reflect.Type{
	"tokenRequest": reflect.TypeFor[tokenRequest](),
	"openRequest":  reflect.TypeFor[openRequest](),
	"shareRequest": reflect.TypeFor[shareRequest](),
	"codeRequest":  reflect.TypeFor[codeRequest](),
}

// TestRequestTypesFlat checks checkMembers' preconditions for every type
// that the package's code passes to decodeJSON: no embedded struct, and
// no map whose keys are not plain strings. It finds the calls in the
// source, each with a variable declared as var name T in its function.
func TestRequestTypesFlat(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	called := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, call := range decodeCalls(fn.Body) {
				typ := declaredType(fn.Body, call)
				switch {
				case typ == "":
					t.Errorf("%s: decodeJSON's value is not &name, with name declared as var name T", fset.Position(call.Pos()))
				case requestTypes[typ] == nil:
					t.Errorf("%s: decodeJSON decodes into %s, which requestTypes does not list", fset.Position(call.Pos()), typ)
				}
				called[typ] = true
			}
		}
	}
	for name, typ := range requestTypes {
		if !called[name] {
			t.Errorf("requestTypes lists %s, which no decodeJSON call decodes into", name)
		}
		if p := notFlat(typ, map[reflect.Type]bool{}); p != "" {
			t.Errorf("%s is not flat, which checkMembers assumes: %s", name, p)
		}
	}
	// The check itself finds what it is for.
	for _, typ := range []reflect.Type{reflect.TypeFor[strictRequest](), reflect.TypeFor[struct{ M map[int]string }](), reflect.TypeFor[struct{ L []map[netip.Addr]int }]()} {
		if notFlat(typ, map[reflect.Type]bool{}) == "" {
			t.Errorf("notFlat(%v) found nothing", typ)
		}
	}
}

// decodeCalls returns the calls of decodeJSON in body.
func decodeCalls(body *ast.BlockStmt) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "decodeJSON" && len(call.Args) == 3 {
				calls = append(calls, call)
			}
		}
		return true
	})
	return calls
}

// declaredType returns the name of T for a call of decodeJSON whose value
// is &name, where body declares var name T; or "" otherwise.
func declaredType(body *ast.BlockStmt, call *ast.CallExpr) string {
	u, ok := call.Args[2].(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return ""
	}
	v, ok := u.X.(*ast.Ident)
	if !ok {
		return ""
	}
	typ := ""
	ast.Inspect(body, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range spec.Names {
			if id, ok := spec.Type.(*ast.Ident); ok && name.Name == v.Name {
				typ = id.Name
			}
		}
		return true
	})
	return typ
}

// notFlat describes what in t, a type that a body decodes into, breaks
// checkMembers' preconditions, or returns "".
func notFlat(t reflect.Type, seen map[reflect.Type]bool) string {
	t = memberType(t)
	if t == nil || seen[t] {
		return ""
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Anonymous {
				return fmt.Sprintf("%v embeds %v", t, f.Type)
			}
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			if p := notFlat(f.Type, seen); p != "" {
				return p
			}
		}
	case reflect.Slice, reflect.Array:
		return notFlat(t.Elem(), seen)
	case reflect.Map:
		if k := t.Key(); k.Kind() != reflect.String || memberType(k) == nil {
			return fmt.Sprintf("%v has keys of %v, not plain strings", t, k)
		}
		return notFlat(t.Elem(), seen)
	}
	return ""
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
