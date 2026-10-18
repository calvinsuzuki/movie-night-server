package stream

import (
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Ticket states a viewer page can see while asking to join.
const (
	Pending  = "pending"  // waiting for the sharer
	Allowed  = "allowed"  // may connect (again, after a dropped connection)
	Denied   = "denied"   // the sharer said no
	Expired  = "expired"  // nobody answered in time, or the pass wasn't used
	Watching = "watching" // connected
	Left     = "left"     // the connection ended
	Kicked   = "kicked"   // the sharer removed them
	Stopped  = "stopped"  // the sharer stopped sharing
)

const (
	maxPending   = 5
	askTimeout   = 2 * time.Minute
	allowedTTL   = time.Minute // to use the pass after Allow
	perIPLimit   = 3           // join requests per IP per minute
	forgetAfter  = 10 * time.Minute
	maxNameRunes = 32
)

var (
	ErrBusy     = errors.New("too many people are waiting, try again in a minute")
	ErrNoTicket = errors.New("not allowed in (yet)")
)

// Request is someone waiting to be let in, as shown in the tray.
type Request struct {
	Ticket, Name, IP string
	At               time.Time
}

type ticket struct {
	Request
	state   string
	decided time.Time
	viewer  string
	wake    chan struct{} // closed and replaced on every state change
}

// Lobby holds join requests for one share session. Nobody connects without
// a ticket the sharer allowed, and each ticket is good for one connection at
// a time.
type Lobby struct {
	mu      sync.Mutex
	token   string // the secret part of the link; "" while not sharing
	approve bool
	tickets map[string]*ticket
	byIP    map[string][]time.Time

	// OnChange is called when requests or decisions change; OnRequest also
	// fires for each new request, so the sharer can be notified.
	OnChange  func()
	OnRequest func(Request)
}

func NewLobby() *Lobby {
	l := &Lobby{tickets: map[string]*ticket{}, byIP: map[string][]time.Time{}, approve: true}
	go l.janitor()
	return l
}

// Open starts a share session with a fresh link token and returns it.
func (l *Lobby) Open() string {
	l.Close()
	l.mu.Lock()
	l.token = newToken()
	t := l.token
	l.mu.Unlock()
	return t
}

// Close ends the session: the link stops working and waiting pages are told.
// It returns how many pages were still waiting or watching.
func (l *Lobby) Close() int {
	l.mu.Lock()
	l.token = ""
	told := 0
	for _, t := range l.tickets {
		if t.state == Pending || t.state == Allowed || t.state == Watching {
			told++
		}
		l.set(t, Stopped)
	}
	l.tickets = map[string]*ticket{}
	l.byIP = map[string][]time.Time{}
	l.mu.Unlock()
	l.changed()
	return told
}

// Token is the current link token, or "" while not sharing.
func (l *Lobby) Token() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.token
}

// SetApproval turns "ask the sharer first" on or off.
func (l *Lobby) SetApproval(on bool) {
	l.mu.Lock()
	l.approve = on
	l.mu.Unlock()
}

// Join files a request to watch and returns its ticket.
func (l *Lobby) Join(name, ip string) (string, error) {
	name = cleanName(name)
	now := time.Now()
	l.mu.Lock()
	recent := l.byIP[ip][:0]
	for _, at := range l.byIP[ip] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	l.byIP[ip] = recent
	pending := 0
	for _, t := range l.tickets {
		if t.state == Pending {
			pending++
		}
	}
	if len(recent) >= perIPLimit || pending >= maxPending {
		l.mu.Unlock()
		return "", ErrBusy
	}
	l.byIP[ip] = append(recent, now)
	t := &ticket{Request: Request{Ticket: newToken(), Name: name, IP: ip, At: now}, state: Pending, wake: make(chan struct{})}
	l.tickets[t.Ticket] = t
	if !l.approve {
		l.set(t, Allowed)
	}
	ask := l.approve
	l.mu.Unlock()
	if ask && l.OnRequest != nil {
		l.OnRequest(t.Request)
	}
	l.changed()
	return t.Ticket, nil
}

// Wait returns the ticket's state once it differs from while (usually
// Pending), or after timeout (long polling). Unknown tickets report Stopped.
func (l *Lobby) Wait(id, while string, timeout time.Duration) string {
	l.mu.Lock()
	t, ok := l.tickets[id]
	if !ok {
		l.mu.Unlock()
		return Stopped
	}
	state, wake := t.state, t.wake
	l.mu.Unlock()
	if state != while {
		return state
	}
	select {
	case <-wake:
	case <-time.After(timeout):
	}
	return l.State(id)
}

func (l *Lobby) State(id string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.tickets[id]; ok {
		return t.state
	}
	return Stopped
}

// Cancel withdraws a request (the page's Cancel button).
func (l *Lobby) Cancel(id string) {
	l.decide(id, Pending, Left)
}

func (l *Lobby) Allow(id string) { l.decide(id, Pending, Allowed) }
func (l *Lobby) Deny(id string)  { l.decide(id, Pending, Denied) }

func (l *Lobby) decide(id, from, to string) {
	l.mu.Lock()
	t, ok := l.tickets[id]
	if ok && t.state == from {
		l.set(t, to)
	}
	l.mu.Unlock()
	l.changed()
}

// Use spends an allowed ticket on a connection and returns who it belongs to.
func (l *Lobby) Use(id string) (Request, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.tickets[id]
	if !ok || t.state != Allowed {
		return Request{}, ErrNoTicket
	}
	l.set(t, Watching)
	return t.Request, nil
}

// Release ends a ticket whose connection couldn't be set up.
func (l *Lobby) Release(id string) { l.decide(id, Watching, Left) }

// Attach links a used ticket to the viewer connection it opened.
func (l *Lobby) Attach(id, viewer string) {
	l.mu.Lock()
	if t, ok := l.tickets[id]; ok {
		t.viewer = viewer
	}
	l.mu.Unlock()
}

// Viewer returns the connection a ticket is watching through, if any.
func (l *Lobby) Viewer(id string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.tickets[id]
	if !ok || t.state != Watching || t.viewer == "" {
		return "", false
	}
	return t.viewer, true
}

// Kicked marks the viewer's ticket so their page doesn't silently ask again.
func (l *Lobby) Kicked(viewer string) { l.ended(viewer, Kicked) }

// Gone records that a viewer's connection ended. The ticket becomes a fresh
// pass for allowedTTL, so a page whose network blipped reconnects without the
// sharer having to let them in again.
func (l *Lobby) Gone(viewer string) { l.ended(viewer, Allowed) }

func (l *Lobby) ended(viewer, state string) {
	l.mu.Lock()
	for _, t := range l.tickets {
		if t.viewer == viewer && t.state == Watching {
			l.set(t, state)
		}
	}
	l.mu.Unlock()
}

// Requests lists the people waiting for an answer, oldest first.
func (l *Lobby) Requests() []Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	var rs []Request
	for _, t := range l.tickets {
		if t.state == Pending {
			rs = append(rs, t.Request)
		}
	}
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].At.Before(rs[j-1].At); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	return rs
}

// set changes a ticket's state and wakes pages waiting on it; l.mu must be held.
func (l *Lobby) set(t *ticket, state string) {
	t.state, t.decided = state, time.Now()
	close(t.wake)
	t.wake = make(chan struct{})
}

func (l *Lobby) changed() {
	if l.OnChange != nil {
		l.OnChange()
	}
}

// janitor expires unanswered requests and unused passes, and forgets old tickets.
func (l *Lobby) janitor() {
	for range time.Tick(5 * time.Second) {
		now := time.Now()
		changed := false
		l.mu.Lock()
		for id, t := range l.tickets {
			switch {
			case t.state == Pending && now.Sub(t.At) > askTimeout,
				t.state == Allowed && now.Sub(t.decided) > allowedTTL:
				l.set(t, Expired)
				changed = true
			case t.state != Pending && t.state != Allowed && t.state != Watching && now.Sub(t.decided) > forgetAfter:
				delete(l.tickets, id)
			}
		}
		l.mu.Unlock()
		if changed {
			l.changed()
		}
	}
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\u202e' || r == '\u202d' { // right-to-left tricks
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxNameRunes {
		s = string(r[:maxNameRunes])
	}
	if s == "" {
		s = "A friend"
	}
	return s
}
