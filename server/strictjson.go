// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// maxJSONDepth bounds how deeply a request body's arrays and objects
// nest. API v1's requests nest two deep at most; encoding/json allows
// 10000.
const maxJSONDepth = 32

// checkMembers checks the members of every object in the first JSON
// value of data against t, the type the value decodes into, before
// encoding/json decodes it: a member that appears twice is an error, and
// so is one whose name is not exactly, in case, a field of the struct it
// fills. encoding/json takes the last of two members and matches names
// in any case, so {"connection":"a","Connection":"b"} would fill
// Connection with b, while a proxy in front of occamyd that reads the
// first member, or matches names exactly, would see a. Refusing both
// leaves every reader of a body one reading of it.
//
// A syntax error is left for the decoder to report, as it would without
// this check.
//
// The check is exact for types that are flat in two ways, which
// TestRequestTypesFlat checks for every type decodeJSON decodes into.
// No struct embeds another: jsonFields gives a name that two embedded
// structs share to the first, where encoding/json gives it to the
// shallower, or to neither. And every map's keys are plain strings:
// keys are compared as the JSON strings they are, where encoding/json
// reads "1" and "01" as the same key of a map[int].
func checkMembers(data []byte, t reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err := checkValue(dec, t, 0)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) || errors.Is(err, errUnexpectedEnd) {
		return nil
	}
	return err
}

// errUnexpectedEnd is what Token's io.ErrUnexpectedEOF and io.EOF become:
// a body that ends early, which the decoder reports itself.
var errUnexpectedEnd = errors.New("unexpected end of JSON input")

// checkValue checks the value whose first token dec reads next, of type
// t, or of no type the check knows if t is nil.
func checkValue(dec *json.Decoder, t reflect.Type, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return tokenError(err)
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth++; depth > maxJSONDepth {
		return fmt.Errorf("the body nests deeper than %d", maxJSONDepth)
	}
	t = memberType(t)
	switch d {
	case '{':
		return checkObject(dec, t, depth)
	case '[':
		var elem reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			elem = t.Elem()
		}
		for dec.More() {
			if err := checkValue(dec, elem, depth); err != nil {
				return err
			}
		}
		_, err := dec.Token() // ]
		return tokenError(err)
	}
	return nil
}

// checkObject checks the members of an object that fills t, whose {
// dec has read.
func checkObject(dec *json.Decoder, t reflect.Type, depth int) error {
	var fields map[string]reflect.Type
	var elem reflect.Type
	if t != nil {
		switch t.Kind() {
		case reflect.Struct:
			fields = jsonFields(t)
		case reflect.Map:
			elem = t.Elem()
		}
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return tokenError(err)
		}
		name, _ := tok.(string)
		if seen[name] {
			return fmt.Errorf("field %q appears more than once", name)
		}
		seen[name] = true
		mt := elem
		if fields != nil {
			ft, ok := fields[name]
			if !ok {
				return unknownField(name, fields)
			}
			mt = ft
		}
		if err := checkValue(dec, mt, depth); err != nil {
			return err
		}
	}
	_, err := dec.Token() // }
	return tokenError(err)
}

// unknownField describes a member that names no field of fields
// exactly, naming the field it names in another case, if any.
func unknownField(name string, fields map[string]reflect.Type) error {
	for f := range fields {
		if strings.EqualFold(f, name) {
			return fmt.Errorf("unknown field %q: field names are case-sensitive, did you mean %q?", name, f)
		}
	}
	return fmt.Errorf("unknown field %q", name)
}

// tokenError turns the end of the input into errUnexpectedEnd.
func tokenError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errUnexpectedEnd
	}
	return err
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// memberType is the type whose members the check knows for a value that
// decodes into t: t without pointers, or nil for a type that decodes
// itself, and so takes members the check cannot know, or for nil.
func memberType(t reflect.Type) reflect.Type {
	for t != nil {
		if t.Implements(jsonUnmarshaler) || reflect.PointerTo(t).Implements(jsonUnmarshaler) ||
			t.Implements(textUnmarshaler) || reflect.PointerTo(t).Implements(textUnmarshaler) {
			return nil
		}
		if t.Kind() != reflect.Pointer {
			return t
		}
		t = t.Elem()
	}
	return nil
}

// jsonFields returns the JSON names of struct t's fields, as
// encoding/json names them, with their types: the tag's name, or the
// field's own; fields of embedded structs without a tag as if they were
// t's; and no field tagged "-" or unexported.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if ft := memberType(f.Type); ft != nil && ft.Kind() == reflect.Struct {
				for n, typ := range jsonFields(ft) {
					if _, ok := fields[n]; !ok {
						fields[n] = typ
					}
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}
