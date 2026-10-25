package stream

import (
	"errors"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

// Emojis viewers may throw at the screen; the button starts as popcorn and
// the arrow next to it picks another.
var Emojis = []string{"🍿", "😆", "😤", "🤮", "😭", "🤭", "🌚", "👋", "👌", "🤌", "👊", "👏", "💩", "👀"}

var (
	ErrEmoji     = errors.New("can't throw that")
	ErrThrowRate = errors.New("slow down")
)

const (
	keepThrows = 50  // recent throws kept for pages catching up
	throwBurst = 6   // throws in a row before the rate limit kicks in
	throwRate  = 2   // throws per second after that
	minFlight  = 300 // ms in the air, picked at random per throw
	maxFlight  = 800
)

// Throw is one emoji flying at everyone's screen. The sounds and the spot it
// hits are picked here, so every viewer sees and hears the same throw.
type Throw struct {
	Seq    uint64  `json:"seq"`
	Emoji  string  `json:"emoji"`
	Name   string  `json:"name"`
	Throw  string  `json:"throw"` // sound file played at launch
	Hit    string  `json:"hit"`   // sound file played on impact
	X      float64 `json:"x"`     // where it lands, 0–1 across the screen
	Y      float64 `json:"y"`
	Flight int     `json:"flight"` // ms from the throw sound to the hit
}

type bucket struct {
	tokens float64
	at     time.Time
}

// Reactions is the feed of throws for one share session.
type Reactions struct {
	mu      sync.Mutex
	seq     uint64
	recent  []Throw
	wake    chan struct{} // closed and replaced on every throw
	throwW  map[string]float64
	hitW    map[string]float64
	buckets map[string]*bucket // per ticket

	// OnThrow is called for every throw (the sharer sees them too).
	OnThrow func(Throw)
}

func NewReactions() *Reactions {
	return &Reactions{wake: make(chan struct{}), buckets: map[string]*bucket{}}
}

// SetSounds sets the odds of each throw and hit sound: relative weights, so
// {"a.mp3": 3, "b.mp3": 1} plays a three times as often as b; 0 means never.
func (r *Reactions) SetSounds(throw, hit map[string]float64) {
	r.mu.Lock()
	r.throwW, r.hitW = throw, hit
	r.mu.Unlock()
}

// Files lists the sounds that can come up, for pages to preload.
func (r *Reactions) Files() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, m := range []map[string]float64{r.throwW, r.hitW} {
		for f, w := range m {
			if w > 0 {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Add throws emoji on behalf of the viewer holding ticket.
func (r *Reactions) Add(ticket, name, emoji string) (Throw, error) {
	ok := false
	for _, e := range Emojis {
		ok = ok || e == emoji
	}
	if !ok {
		return Throw{}, ErrEmoji
	}
	r.mu.Lock()
	now := time.Now()
	b := r.buckets[ticket]
	if b == nil {
		b = &bucket{tokens: throwBurst, at: now}
		r.buckets[ticket] = b
	}
	b.tokens = min(throwBurst, b.tokens+now.Sub(b.at).Seconds()*throwRate)
	b.at = now
	if b.tokens < 1 {
		r.mu.Unlock()
		return Throw{}, ErrThrowRate
	}
	b.tokens--

	r.seq++
	t := Throw{
		Seq: r.seq, Emoji: emoji, Name: name,
		Throw: pick(r.throwW), Hit: pick(r.hitW),
		X: 0.2 + 0.6*rand.Float64(), Y: 0.15 + 0.55*rand.Float64(),
		Flight: minFlight + rand.IntN(maxFlight-minFlight+1),
	}
	r.recent = append(r.recent, t)
	if len(r.recent) > keepThrows {
		r.recent = r.recent[len(r.recent)-keepThrows:]
	}
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
	if r.OnThrow != nil {
		r.OnThrow(t)
	}
	return t, nil
}

// Since returns the throws after seq, waiting up to timeout for one (long
// polling), and the newest seq to ask from next time.
func (r *Reactions) Since(seq uint64, timeout time.Duration) ([]Throw, uint64) {
	r.mu.Lock()
	cur, wake := r.seq, r.wake
	r.mu.Unlock()
	if cur == seq {
		select {
		case <-wake:
		case <-time.After(timeout):
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Throw{}
	for _, t := range r.recent {
		if t.Seq > seq {
			out = append(out, t)
		}
	}
	return out, r.seq
}

// Seq is the newest throw's number, where a page that just joined starts.
func (r *Reactions) Seq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Clear forgets the session's throws and rate limits.
func (r *Reactions) Clear() {
	r.mu.Lock()
	r.recent, r.buckets = nil, map[string]*bucket{}
	r.mu.Unlock()
}

// pick returns a key at random, weighted by its value, or "" if none has a
// positive weight.
func pick(weights map[string]float64) string {
	keys := make([]string, 0, len(weights))
	total := 0.0
	for k, w := range weights {
		if w > 0 {
			keys = append(keys, k)
			total += w
		}
	}
	sort.Strings(keys) // map order is random; keep draws reproducible in tests
	x := rand.Float64() * total
	for _, k := range keys {
		if x -= weights[k]; x < 0 {
			return k
		}
	}
	if len(keys) > 0 {
		return keys[len(keys)-1]
	}
	return ""
}
