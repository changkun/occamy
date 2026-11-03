// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package decoders_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/client/decoders"
	"changkun.de/x/occamy/protocol"
)

// The decoders against the codec vectors that occamyd's encoders wrote:
// lossless ones exact, lossy ones at their min_psnr, a rectangle other
// than the payload's refused before decoding, and the invalid payloads
// refused.
func TestVectors(t *testing.T) {
	b, err := os.ReadFile("../../protocol/testdata/codecs.json")
	if err != nil {
		t.Fatal(err)
	}
	type vector struct {
		Name          string
		Codec         protocol.Codec
		Width, Height int
		Pixels        string
		Payload       string
		Exact         bool
		MinPSNR       float64 `json:"min_psnr"`
	}
	var file struct{ Vectors, Invalid []vector }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	byCodec := map[protocol.Codec]client.Decoder{}
	for _, d := range decoders.All() {
		byCodec[d.Codec()] = d
	}
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	checked := 0
	for _, v := range file.Vectors {
		d, ok := byCodec[v.Codec]
		if !ok {
			continue // raw, which the client core decodes itself
		}
		want := unhex(v.Pixels)
		if v.Codec == protocol.CodecWebP && !opaque(want) {
			continue // a lossless WebP cursor, which a client of All never gets
		}
		t.Run(v.Name, func(t *testing.T) {
			got := make([]byte, 4*v.Width*v.Height)
			if err := d.Decode(got, v.Width, v.Height, unhex(v.Payload)); err != nil {
				t.Fatal(err)
			}
			if v.Exact {
				if !bytes.Equal(got, want) {
					t.Errorf("pixels\n%x\nwant\n%x", got, want)
				}
				return
			}
			if p := psnr(got, want); p < v.MinPSNR {
				t.Errorf("PSNR %.1f dB, want at least %g", p, v.MinPSNR)
			}
			if err := d.Decode(make([]byte, 4*(v.Width+1)*v.Height), v.Width+1, v.Height, unhex(v.Payload)); err == nil {
				t.Errorf("a %d×%d payload decoded into %d×%d", v.Width, v.Height, v.Width+1, v.Height)
			}
		})
		checked++
	}
	if checked < 10 {
		t.Errorf("checked %d vectors", checked)
	}
	for _, v := range file.Invalid {
		t.Run("invalid/"+v.Name, func(t *testing.T) {
			d, ok := byCodec[v.Codec]
			if !ok {
				t.Fatalf("no decoder of codec %v", v.Codec)
			}
			if err := d.Decode(make([]byte, 4*v.Width*v.Height), v.Width, v.Height, unhex(v.Payload)); err == nil {
				t.Error("decoded")
			}
		})
	}
}

// opaque reports whether every pixel's alpha is 255.
func opaque(pix []byte) bool {
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return false
		}
	}
	return true
}

// psnr compares the colour channels, as internal/encoder's golden test
// does.
func psnr(a, b []byte) float64 {
	var sum float64
	n := 0
	for i := 0; i < len(a); i += 4 {
		for k := range 3 {
			d := float64(a[i+k]) - float64(b[i+k])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}
