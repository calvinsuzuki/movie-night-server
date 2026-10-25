package stream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerRules(t *testing.T) {
	hub, _ := NewHub()
	l := NewLobby()
	page := fstest.MapFS{"index.html": {Data: []byte("viewer")}}
	srv := httptest.NewServer(Handler(hub, l, NewReactions(), "Host", page, map[string][]byte{"join-sound.wav": []byte("RIFF")}))
	defer srv.Close()
	token := l.Open()
	s := srv.URL + "/s/" + token + "/"

	do := func(method, url, body string, hdr ...string) int {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for _, c := range []struct {
		name, method, url, body string
		hdr                     []string
		want                    int
	}{
		{"page", "GET", s, "", nil, 200},
		{"wrong token", "GET", srv.URL + "/s/0123/", "", nil, 404},
		{"root", "GET", srv.URL + "/", "", nil, 404},
		{"config", "GET", s + "config", "", nil, 200},
		{"extra file", "GET", s + "join-sound.wav", "", nil, 200},
		{"watchers without watching", "GET", s + "watchers?ticket=nope", "", nil, 403},
		{"throw without watching", "POST", s + "throw", `{"ticket":"nope","emoji":"🍿"}`, nil, 403},
		{"throws without watching", "GET", s + "throws?ticket=nope", "", nil, 403},
		{"join", "POST", s + "join", `{"name":"Eve"}`, nil, 201},
		{"whep without ticket", "POST", s + "whep", "v=0", nil, 403},
		{"whep with bogus ticket", "POST", s + "whep", "v=0", []string{"X-Ticket", "nope"}, 403},
	} {
		if got := do(c.method, c.url, c.body, c.hdr...); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}

	// A pending (not yet allowed) ticket can't connect either.
	tk, _ := l.Join("Frank", "5.5.5.5")
	if got := do("POST", s+"whep", "v=0", "X-Ticket", tk); got != 403 {
		t.Errorf("pending ticket: %d, want 403", got)
	}

	// Browser reports: only from someone who asked to join, and only a few.
	if got := do("POST", s+"report", `{"ticket":"nope","reason":"x"}`); got != 403 {
		t.Errorf("report without ticket: %d, want 403", got)
	}
	for i := 1; i <= 6; i++ {
		want := 204
		if i == 6 {
			want = 403
		}
		if got := do("POST", s+"report", `{"ticket":"`+tk+`","reason":"never connected\nfake line","pairs":["a","b"]}`); got != want {
			t.Errorf("report %d: %d, want %d", i, got, want)
		}
	}

	l.Close()
	if got := do("GET", s, "", nil...); got != 404 {
		t.Errorf("link after stop: %d, want 404", got)
	}
}
