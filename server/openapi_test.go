// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// openAPIDoc is the OpenAPI description of the routes, relative to this
// package.
var openAPIDoc = filepath.Join("..", "docs", "openapi.yaml")

// openAPIMethods are the operations a path item of OpenAPI may hold.
var openAPIMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// TestOpenAPIRoutes checks that docs/openapi.yaml has an operation for
// each route that TestAPIDoc checks, with every optional route on and the
// debug listener's, and no operation for a route that occamyd does not
// register.
func TestOpenAPIRoutes(t *testing.T) {
	doc := readOpenAPI(t)
	routes := registeredRoutes(t)
	var described []string
	for path, item := range object(t, doc, "paths") {
		for _, m := range openAPIMethods {
			if _, ok := item.(map[string]any)[m]; ok {
				described = append(described, strings.ToUpper(m)+" "+path)
			}
		}
	}
	for _, r := range routes {
		if !slices.Contains(described, r) {
			t.Errorf("%s has no operation for %s", openAPIDoc, r)
		}
	}
	for _, d := range described {
		if !slices.Contains(routes, d) {
			t.Errorf("%s describes %s, which occamyd does not register on either listener", openAPIDoc, d)
		}
	}
}

// TestOpenAPIRefs checks that every $ref of docs/openapi.yaml points
// into the file, at something there, and that operation IDs are unique.
func TestOpenAPIRefs(t *testing.T) {
	doc := readOpenAPI(t)
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if _, err := resolve(doc, ref); err != "" {
					t.Errorf("%s: %s", at, err)
				}
			}
			for k, e := range v {
				walk(e, at+"/"+k)
			}
		case []any:
			for i, e := range v {
				walk(e, at+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(doc, "#")

	ids := map[string]string{}
	for path, item := range object(t, doc, "paths") {
		for _, m := range openAPIMethods {
			op, ok := item.(map[string]any)[m].(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			route := strings.ToUpper(m) + " " + path
			switch {
			case id == "":
				t.Errorf("%s has no operationId", route)
			case ids[id] != "":
				t.Errorf("%s and %s share the operationId %q", ids[id], route, id)
			default:
				ids[id] = route
			}
		}
	}
}

// openAPISchemas names the Go type of each JSON body that
// docs/openapi.yaml describes. Requests are decoded by decodeJSON, which
// refuses fields the type lacks, so their schemas must forbid them too.
var openAPISchemas = []struct {
	name    string
	v       any
	request bool
}{
	{"TokenRequest", tokenRequest{}, true},
	{"CodeRequest", codeRequest{}, true},
	{"TokenResponse", tokenResponse{}, false},
	{"AuthMethods", signInMethods{}, false},
	{"Connection", connectionInfo{}, false},
	{"ConnectionList", connectionList{}, false},
	{"OpenSessionRequest", openRequest{}, true},
	{"OpenSessionResponse", openResponse{}, false},
	{"Session", sessionStateResponse{}, false},
	{"SessionSize", sessionSize{}, false},
	{"Viewer", viewerInfo{}, false},
	{"ShareRequest", shareRequest{}, true},
	{"Share", shareResponse{}, false},
	{"Error", apiError{}, false},
	{"ErrorBody", errorBody{}, false},
}

// TestOpenAPISchemas checks the schemas of docs/openapi.yaml against the
// Go types of the bodies: the same fields, by their JSON names; a field
// required exactly when the type always sends it, without omitempty; and
// each field of the JSON type its Go type encodes to, a nested struct by
// a reference to its own schema. A schema with oneOf is checked as the
// union of its alternatives' fields, those that all of them require
// being required.
func TestOpenAPISchemas(t *testing.T) {
	doc := readOpenAPI(t)
	schemas := object(t, doc, "components", "schemas")
	names := map[reflect.Type]string{}
	for _, s := range openAPISchemas {
		names[reflect.TypeOf(s.v)] = s.name
	}
	for _, s := range openAPISchemas {
		t.Run(s.name, func(t *testing.T) {
			schema, ok := schemas[s.name].(map[string]any)
			if !ok {
				t.Fatalf("%s has no schema %s for %T", openAPIDoc, s.name, s.v)
			}
			alts := []map[string]any{schema}
			if one, ok := schema["oneOf"].([]any); ok {
				alts = nil
				for _, a := range one {
					alts = append(alts, a.(map[string]any))
				}
			}
			props := map[string]map[string]any{}
			var required []string
			for i, a := range alts {
				if s.request && a["additionalProperties"] != false {
					t.Errorf("a request: additionalProperties must be false, since the route refuses unknown fields")
				}
				p, _ := a["properties"].(map[string]any)
				for k, v := range p {
					props[k] = v.(map[string]any)
				}
				req := yamlStrings(a["required"])
				if i == 0 {
					required = req
				} else {
					required = slices.DeleteFunc(required, func(r string) bool { return !slices.Contains(req, r) })
				}
			}

			typ := reflect.TypeOf(s.v)
			var fields, always []string
			for f := range typ.Fields() {
				name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
				if name == "-" || !f.IsExported() {
					continue
				}
				fields = append(fields, name)
				if !slices.Contains(strings.Split(opts, ","), "omitempty") {
					always = append(always, name)
				}
				prop, ok := props[name]
				if !ok {
					continue
				}
				if err := jsonType(doc, prop, f.Type, names); err != "" {
					t.Errorf("%s (%s %s): %s", name, f.Name, f.Type, err)
				}
			}
			slices.Sort(fields)
			slices.Sort(always)
			described := slices.Sorted(maps.Keys(props))
			slices.Sort(required)
			if !slices.Equal(described, fields) {
				t.Errorf("properties %v, want %T's JSON fields %v", described, s.v, fields)
			}
			if !slices.Equal(required, always) {
				t.Errorf("required %v, want the fields without omitempty %v", required, always)
			}
		})
	}
}

// jsonType reports how prop fails to describe the JSON that encoding/json
// makes of a t, or "" if it does not.
func jsonType(doc map[string]any, prop map[string]any, t reflect.Type, names map[reflect.Type]string) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if ref, ok := prop["$ref"].(string); ok {
		target, err := resolve(doc, ref)
		if err != "" {
			return err
		}
		if t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]() {
			if want := "#/components/schemas/" + names[t]; names[t] == "" || ref != want {
				return "refers to " + ref + ", want the schema of " + t.String()
			}
			return ""
		}
		prop = target
	}
	want := ""
	switch t.Kind() {
	case reflect.String:
		want = "string"
	case reflect.Bool:
		want = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		want = "integer"
	case reflect.Slice:
		want = "array"
	case reflect.Struct:
		if t != reflect.TypeFor[time.Time]() {
			return "a struct must refer to its own schema"
		}
		if prop["type"] != "string" || prop["format"] != "date-time" {
			return "a time.Time is a string of format date-time"
		}
		return ""
	default:
		return "no JSON type for kind " + t.Kind().String()
	}
	if prop["type"] != want {
		return "type " + strconv.Quote(str(prop["type"])) + ", want " + strconv.Quote(want)
	}
	if t.Kind() == reflect.Slice {
		items, ok := prop["items"].(map[string]any)
		if !ok {
			return "an array without items"
		}
		return jsonType(doc, items, t.Elem(), names)
	}
	return ""
}

// errorConst matches the name of an API error code's constant.
var errorConst = regexp.MustCompile(`^code[A-Z]`)

// TestOpenAPIErrorCodes checks that the error codes of
// docs/openapi.yaml, ErrorCode's for JSON bodies and OIDCError's for the
// fragment of an OIDC sign-in's return URL, are the package's code
// constants, each in one of the two, and that docs/api.md's table of
// errors lists the same codes.
func TestOpenAPIErrorCodes(t *testing.T) {
	doc := readOpenAPI(t)
	schemas := object(t, doc, "components", "schemas")
	inBody := yamlStrings(schemas["ErrorCode"].(map[string]any)["enum"])
	inFragment := yamlStrings(schemas["OIDCError"].(map[string]any)["enum"])
	for _, c := range inBody {
		if slices.Contains(inFragment, c) {
			t.Errorf("%s is both an ErrorCode and an OIDCError", c)
		}
	}
	described := slices.Sorted(slices.Values(slices.Concat(inBody, inFragment)))

	declared := errorCodes(t)
	if !slices.Equal(described, declared) {
		t.Errorf("%s has the error codes %v, want the code constants of package server %v", openAPIDoc, described, declared)
	}
	if listed := apiDocErrorCodes(t); !slices.Equal(listed, declared) {
		t.Errorf("%s's Errors table has the codes %v, want the code constants of package server %v", apiDoc, listed, declared)
	}
}

// errorCodes returns the values of the package's string constants named
// code and a capital, which are API v1's error codes, sorted.
func errorCodes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if !errorConst.MatchString(n.Name) || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					codes = append(codes, v)
				}
			}
		}
	}
	slices.Sort(codes)
	if len(codes) == 0 {
		t.Fatal("no error code constants found")
	}
	return slices.Compact(codes)
}

// errorRow matches a row of docs/api.md's table of errors, which starts
// with the code in a code span, as its header does.
var errorRow = regexp.MustCompile("^\\| `([a-z_]+)` \\|")

// apiDocErrorCodes returns the codes of the table under docs/api.md's
// "## Errors", sorted and without repeats.
func apiDocErrorCodes(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(apiDoc)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var codes []string
	in, body := false, false // in the section, and in a table's body
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "## "):
			in, body = line == "## Errors", false
		case !strings.HasPrefix(line, "|"):
			body = false
		case strings.HasPrefix(line, "|--"):
			body = true
		default:
			if m := errorRow.FindStringSubmatch(line); in && body && m != nil {
				codes = append(codes, m[1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

// readOpenAPI reads docs/openapi.yaml.
func readOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(openAPIDoc)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", openAPIDoc, err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("%s: openapi %v, want 3.1.0", openAPIDoc, doc["openapi"])
	}
	return doc
}

// object returns the object at keys in doc, or fails the test.
func object(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()
	v := doc
	for _, k := range keys {
		next, ok := v[k].(map[string]any)
		if !ok {
			t.Fatalf("%s has no object %s", openAPIDoc, strings.Join(keys, "."))
		}
		v = next
	}
	return v
}

// resolve returns the object that a local $ref names, or why it cannot.
func resolve(doc map[string]any, ref string) (map[string]any, string) {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, "$ref " + ref + " points out of the file"
	}
	v := doc
	for _, k := range strings.Split(rest, "/") {
		next, ok := v[strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")].(map[string]any)
		if !ok {
			return nil, "$ref " + ref + " points at nothing"
		}
		v = next
	}
	return v, ""
}

// strings2 returns v, a YAML sequence of strings, as a slice.
func yamlStrings(v any) []string {
	var s []string
	for _, e := range v.([]any) {
		s = append(s, e.(string))
	}
	return s
}

// str returns v as a string, or "" if it is none.
func str(v any) string {
	s, _ := v.(string)
	return s
}
