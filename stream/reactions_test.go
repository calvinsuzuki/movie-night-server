package stream

import (
	"math"
	"testing"
	"time"
)

func TestPickWeights(t *testing.T) {
	w := map[string]float64{"a": 3, "b": 1, "never": 0, "negative": -2}
	got := map[string]int{}
	const n = 20000
	for i := 0; i < n; i++ {
		got[pick(w)]++
	}
	if got["never"] != 0 || got["negative"] != 0 {
		t.Fatalf("zero or negative weight was picked: %v", got)
	}
	if share := float64(got["a"]) / n; math.Abs(share-0.75) > 0.02 {
		t.Fatalf("a picked %.3f of the time, want about 0.75", share)
	}
	if s := pick(map[string]float64{"x": 0}); s != "" {
		t.Fatalf("no positive weight should pick nothing, got %q", s)
	}
}

func TestReactions(t *testing.T) {
	r := NewReactions()
	r.SetSounds(map[string]float64{"throw-1.mp3": 1}, map[string]float64{"hit-pan.mp3": 1, "hit-metal.mp3": 0})

	if _, err := r.Add("t1", "Ana", "<b>hi</b>"); err != ErrEmoji {
		t.Fatalf("unlisted emoji: err = %v", err)
	}

	start := r.Seq()
	done := make(chan []Throw)
	go func() { th, _ := r.Since(start, time.Second); done <- th }()
	time.Sleep(10 * time.Millisecond)
	th, err := r.Add("t1", "Ana", "🍿")
	if err != nil {
		t.Fatal(err)
	}
	if th.Throw != "throw-1.mp3" || th.Hit != "hit-pan.mp3" || th.X < 0 || th.X > 1 || th.Y < 0 || th.Y > 1 ||
		th.Flight < minFlight || th.Flight > maxFlight {
		t.Fatalf("throw = %+v", th)
	}
	if got := <-done; len(got) != 1 || got[0].Name != "Ana" || got[0].Emoji != "🍿" {
		t.Fatalf("long poll got %+v", got)
	}
	if got, _ := r.Since(th.Seq, 10*time.Millisecond); len(got) != 0 {
		t.Fatalf("old throws came back: %+v", got)
	}

	// The rate limit is per ticket.
	for i := 1; i < throwBurst; i++ {
		if _, err := r.Add("t1", "Ana", "👀"); err != nil {
			t.Fatalf("throw %d: %v", i, err)
		}
	}
	if _, err := r.Add("t1", "Ana", "👀"); err != ErrThrowRate {
		t.Fatalf("after the burst: err = %v", err)
	}
	if _, err := r.Add("t2", "Bruno", "👀"); err != nil {
		t.Fatalf("someone else's throw: %v", err)
	}
}
