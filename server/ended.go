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

// minEnded is the fewest ended sessions kept, however few run at once.
const minEnded = 1024

// endedSession is what is kept of an ended session.
type endedSession struct {
	id    string
	owner principal
	close protocol.Close // the close the session ended with
	until time.Time      // when it is forgotten
}

// endedSessions holds the sessions that ended within their owner grace,
// at most most, oldest first. The server's mu guards it.
type endedSessions struct {
	// most is the most ended sessions kept: limits.sessions, or minEnded
	// if that is fewer, so that every owner of the most sessions that
	// can end at once, as when a remote they share goes down, still
	// learns how theirs ended. A client that opens sessions whose
	// remotes fail at once pushes the oldest out sooner, which leaves
	// their owners with no such session, as once they expire.
	most  int
	byID  map[string]*endedSession
	order []*endedSession // by until, since every session has the same grace
}

// newEndedSessions keeps up to minEnded ended sessions, or sessions if
// that is more.
func newEndedSessions(sessions int) endedSessions {
	return endedSessions{most: max(minEnded, sessions), byID: map[string]*endedSession{}}
}

// add keeps how session id of owner ended, at now, for grace: the
// session's owner grace, within which an owner who left may come back.
func (e *endedSessions) add(id string, owner principal, c protocol.Close, now time.Time, grace time.Duration) {
	es := &endedSession{id: id, owner: owner, close: c, until: now.Add(grace)}
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
// most.
func (e *endedSessions) prune(now time.Time) {
	n := 0
	for n < len(e.order) && (len(e.order)-n > e.most || !now.Before(e.order[n].until)) {
		delete(e.byID, e.order[n].id)
		e.order[n] = nil
		n++
	}
	e.order = e.order[n:]
}

// len is the number of sessions kept.
func (e *endedSessions) len() int { return len(e.byID) }
