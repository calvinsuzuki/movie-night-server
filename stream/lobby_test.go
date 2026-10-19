package stream

import (
	"testing"
	"time"
)

func TestLobbyApproval(t *testing.T) {
	l := NewLobby()
	l.Open()

	tk, err := l.Join("  Alice\n", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if rs := l.Requests(); len(rs) != 1 || rs[0].Name != "Alice" {
		t.Fatalf("requests = %+v", rs)
	}
	if _, err := l.Use(tk); err == nil {
		t.Fatal("used a ticket nobody allowed")
	}
	if s := l.Wait(tk, Pending, 10*time.Millisecond); s != Pending {
		t.Fatalf("state = %s", s)
	}
	go func() { time.Sleep(20 * time.Millisecond); l.Allow(tk) }()
	if s := l.Wait(tk, Pending, time.Second); s != Allowed {
		t.Fatalf("state after allow = %s", s)
	}
	if _, err := l.Use(tk); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Use(tk); err == nil {
		t.Fatal("ticket worked twice")
	}
	l.Attach(tk, "v1")
	l.Gone("v1") // a dropped connection may come back on the same ticket
	if _, err := l.Use(tk); err != nil {
		t.Fatalf("rejoin after a drop: %v", err)
	}
	l.Attach(tk, "v1")
	l.Kicked("v1")
	l.Gone("v1") // the hub reports the closed connection afterwards
	if s := l.State(tk); s != Kicked {
		t.Fatalf("state after kick = %s", s)
	}

	deny, _ := l.Join("Bob", "2.2.2.2")
	l.Deny(deny)
	l.Allow(deny) // too late: already decided
	if s := l.State(deny); s != Denied {
		t.Fatalf("state after deny = %s", s)
	}
}

func TestLobbyLimits(t *testing.T) {
	l := NewLobby()
	l.Open()
	for i := 0; i < perIPLimit; i++ {
		if _, err := l.Join("x", "9.9.9.9"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Join("x", "9.9.9.9"); err != ErrBusy {
		t.Fatalf("per-IP limit: err = %v", err)
	}
	for i := 0; i < maxPending-perIPLimit; i++ {
		l.Join("y", string(rune('a'+i)))
	}
	if _, err := l.Join("z", "8.8.8.8"); err != ErrBusy {
		t.Fatalf("pending limit: err = %v", err)
	}
}

func TestLobbyClose(t *testing.T) {
	l := NewLobby()
	first := l.Open()
	tk, _ := l.Join("Carol", "3.3.3.3")
	done := make(chan string)
	go func() { done <- l.Wait(tk, Pending, time.Second) }()
	time.Sleep(10 * time.Millisecond)
	l.Close()
	if s := <-done; s != Stopped {
		t.Fatalf("waiting page got %s", s)
	}
	if l.Token() != "" {
		t.Fatal("link still active after Close")
	}
	if second := l.Open(); second == first || len(second) != 32 {
		t.Fatalf("token not renewed: %q", second)
	}
}

func TestLobbyNoApproval(t *testing.T) {
	l := NewLobby()
	l.Open()
	l.SetApproval(false)
	tk, _ := l.Join("Dan", "4.4.4.4")
	if s := l.State(tk); s != Allowed {
		t.Fatalf("state = %s", s)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"":              "A friend",
		"  a\tb  ":      "a b",
		"evil\u202eexe": "evilexe",
		"0123456789012345678901234567890123456789": "01234567890123456789",
		"Bruno the very long name":                 "Bruno the very long", // no trailing space after the cut
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
