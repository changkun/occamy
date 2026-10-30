// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// goldenFile holds payloads of every codec occamyd sends; internal/encoder
// writes it with -update.
var goldenFile = filepath.Join("..", "..", "protocol", "testdata", "codecs.json")

// The decoders of this package decode the vectors of its codecs exactly,
// the container of each deflate vector that occamyd sends is what
// AppendContainer writes, and every invalid vector fails.
func TestGoldenVectors(t *testing.T) {
	b, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Vectors []struct {
			Name          string
			Codec         int
			Width, Height int
			Pixels        string
			Container     string
			Payload       string
			Exact         bool
			Handmade      bool
		}
		Invalid []struct {
			Name          string
			Codec         int
			Width, Height int
			Payload       string
		}
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	unhex := func(name, s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return b
	}
	var in Inflater
	var p Palette
	forms := map[byte]bool{}
	decoded := 0
	for _, v := range g.Vectors {
		if v.Codec != 1 && v.Codec != 2 {
			continue
		}
		pix, payload := unhex(v.Name, v.Pixels), unhex(v.Name, v.Payload)
		out := rgba(make([]byte, 4*v.Width*v.Height), v.Width, v.Height)
		if v.Codec == 1 {
			err = DecodeRaw(out, payload)
		} else {
			err = in.Decode(out, payload)
		}
		if err != nil || !v.Exact || !bytes.Equal(out.Pix, pix) {
			t.Errorf("%s: %v, or the pixels differ", v.Name, err)
		}
		decoded++
		if v.Codec != 2 {
			continue
		}
		container := unhex(v.Name, v.Container)
		if !bytes.Equal(inflate(t, payload), container) {
			t.Errorf("%s: the payload does not inflate to the container", v.Name)
		}
		forms[container[0]] = true
		if v.Handmade {
			continue
		}
		pp := &p
		if !p.Build(pix, v.Width, v.Height) {
			pp = nil
		}
		if got := AppendContainer(nil, pix, v.Width, v.Height, pp); !bytes.Equal(got, container) {
			t.Errorf("%s: container % x, want % x", v.Name, got, container)
		}
	}
	if decoded == 0 || len(forms) != 4 {
		t.Errorf("%d vectors decoded, forms %v; want every form", decoded, forms)
	}
	for _, v := range g.Invalid {
		out := rgba(make([]byte, 4*v.Width*v.Height), v.Width, v.Height)
		if err := in.Decode(out, unhex(v.Name, v.Payload)); v.Codec != 2 || !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid %s: %v, want ErrInvalid", v.Name, err)
		}
	}
}
