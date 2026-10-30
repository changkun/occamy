// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

// docFile is the normative description of the protocol.
var docFile = filepath.Join("..", "docs", "protocol.md")

// codecsFile holds the codec payloads, which internal/encoder writes.
var codecsFile = filepath.Join("testdata", "codecs.json")

// TestDocExamples checks the examples in docs/protocol.md against the
// vectors. An example is a fenced block with the info string "hex" whose
// first line names a message vector ("# vector: <name>"), or a codec
// vector whose deflate container it shows ("# container: <name>"), and
// whose other lines hold hex bytes, with comments from '#' to the end of
// the line. Every message type has an example.
func TestDocExamples(t *testing.T) {
	g := vectorsMessages(t)
	byName := map[string]string{}
	typeOf := map[string]protocol.Type{}
	for _, v := range g.Messages {
		byName["vector: "+v.Name] = v.Hex
		typeOf["vector: "+v.Name] = protocol.Type(v.Type)
	}
	b, err := os.ReadFile(codecsFile)
	if err != nil {
		t.Fatal(err)
	}
	var codecs struct {
		Vectors []struct{ Name, Container string }
	}
	if err := json.Unmarshal(b, &codecs); err != nil {
		t.Fatal(err)
	}
	for _, v := range codecs.Vectors {
		if v.Container != "" {
			byName["container: "+v.Name] = v.Container
		}
	}
	f, err := os.Open(docFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	types := map[protocol.Type]bool{}
	var in bool
	var name string
	var hexDigits strings.Builder
	line := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line++
		text := sc.Text()
		switch {
		case !in && text == "```hex":
			in, name = true, ""
			hexDigits.Reset()
		case in && text == "```":
			in = false
			want, ok := byName[name]
			if !ok {
				t.Errorf("%s:%d: the example names no vector: %q", docFile, line, name)
				continue
			}
			if got := hexDigits.String(); got != want {
				t.Errorf("%s:%d: example %q is\n%s\nwant\n%s", docFile, line, name, got, want)
			}
			if typ, ok := typeOf[name]; ok {
				types[typ] = true
			}
		case in && name == "":
			n, ok := strings.CutPrefix(text, "# ")
			if !ok || !strings.HasPrefix(n, "vector: ") && !strings.HasPrefix(n, "container: ") {
				t.Fatalf("%s:%d: an example starts with %q, want \"# vector: <name>\" or \"# container: <name>\"", docFile, line, text)
			}
			name = n
		case in:
			if i := strings.IndexByte(text, '#'); i >= 0 {
				text = text[:i]
			}
			hexDigits.WriteString(strings.Join(strings.Fields(text), ""))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, typ := range allTypes() {
		if !types[typ] {
			t.Errorf("%s has no example of %s (%#04x)", docFile, typ, uint16(typ))
		}
	}

	doc, err := os.ReadFile(docFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{g.HelloQuery[0].Query, g.Tokens[0].Subprotocol} {
		if !bytes.Contains(doc, []byte(s)) {
			t.Errorf("%s does not show %q", docFile, s)
		}
	}
}
