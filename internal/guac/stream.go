// Copyright 2021 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The following code is modified from
// https://github.com/deluan/bring
// Authored by Deluan Quintao released under MIT license.

package guac

import (
	"bytes"
	"encoding/base64"
	"image"
)

type onEndFunc func(s *stream)

type stream struct {
	buffer *bytes.Buffer
	onEnd  onEndFunc
}

func (s *stream) image() (image.Image, error) {
	dec := base64.NewDecoder(base64.StdEncoding, s.buffer)
	img, _, err := image.Decode(dec)
	return img, err
}

type streams map[int]*stream

func newStreams() streams {
	return make(map[int]*stream)
}

func (ss streams) get(id int) *stream {
	if s, ok := ss[id]; ok {
		return s
	}
	s := &stream{
		buffer: &bytes.Buffer{},
	}
	ss[id] = s
	return s
}

func (ss streams) append(id int, data string) error {
	s := ss.get(id)
	_, err := s.buffer.WriteString(data)
	return err
}

func (ss streams) end(id int) {
	s := ss.get(id)
	if s.onEnd != nil {
		s.onEnd(s)
	}
}

func (ss streams) delete(id int) {
	ss[id].buffer = nil
	ss[id] = nil
	delete(ss, id)
}
