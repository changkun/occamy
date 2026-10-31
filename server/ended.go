// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"time"

	"changkun.de/x/occamy/protocol"
)

// A session's owner may open its stream some time after the session
// began, as after POST /api/v1/sessions, and the session may have ended
// meanwhile: its remote may fail at once. The owner's stream is then
// told how it ended, as it would have been had it come sooner, rather
// than that there is no such session. occamyd keeps that much of each
// session for the owner grace after it ends: the owner, and the close.
// Nobody else learns from it whether a session existed or how it ended.

// Bounds of the ended sessions kept, variables for tests.
var (
	// endedFor is how long an ended session is kept: the owner grace,
	// session.Config.OwnerGrace's default, within which an owner who
	// left may come back.
	endedFor = 60 * time.Second
	// maxEnded is the most ended sessions kept. A client that opens
	// sessions whose remotes fail at once pushes the oldest out sooner,
	// which leaves their owners with no such session, as once they
	// expire.
	maxEnded = 1024
)

// endedSession is what is kept of an ended session.
type endedSession struct {
	id    string
	owner principal
	close protocol.Close // the close the session ended with
	until time.Time      // when it is forgotten
}

// endedSessions holds the sessions that ended within endedFor, at most
// maxEnded, oldest first. The server's mu guards it.
type endedSessions struct {
	byID  map[string]*endedSession
	order []*endedSession // by until, since endedFor is the same for each
}

// add keeps how session id of owner ended, at now.
func (e *endedSessions) add(id string, owner principal, c protocol.Close, now time.Time) {
	if e.byID == nil {
		e.byID = map[string]*endedSession{}
	}
	es := &endedSession{id: id, owner: owner, close: c, until: now.Add(endedFor)}
	e.byID[id] = es
	e.order = append(e.order, es)
	e.prune(now)
}

// get returns how session id ended, if it is still kept at now.
func (e *endedSessions) get(id string, now time.Time) (endedSession, bool) {
	e.prune(now)
	es, ok := e.byID[id]
	if !ok {
		return endedSession{}, false
	}
	return *es, true
}

// prune forgets the sessions that expired by now, and the oldest beyond
// maxEnded.
func (e *endedSessions) prune(now time.Time) {
	n := 0
	for n < len(e.order) && (len(e.order)-n > maxEnded || !now.Before(e.order[n].until)) {
		delete(e.byID, e.order[n].id)
		e.order[n] = nil
		n++
	}
	e.order = e.order[n:]
}

// len is the number of sessions kept.
func (e *endedSessions) len() int { return len(e.byID) }
