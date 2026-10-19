package stream

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Handler serves everything a viewer needs, all under /s/<token>/ so that a
// link stops working when the share session ends:
//
//	GET    /s/{token}/                 viewer page (and its css/js)
//	GET    /s/{token}/config           STUN/TURN servers and throwable emojis for the browser
//	POST   /s/{token}/join             {name} → {ticket}
//	GET    /s/{token}/join/{ticket}    long-poll the sharer's answer (?while=watching: a kick)
//	DELETE /s/{token}/join/{ticket}    withdraw the request
//	POST   /s/{token}/whep             SDP offer (X-Ticket header) → answer
//	DELETE /s/{token}/whep/{id}        leave
//	GET    /s/{token}/watchers         who's watching (?ticket=, long-poll with ?since=)
//	POST   /s/{token}/throw            {ticket, emoji}: throw it at everyone's screen
//	GET    /s/{token}/throws           throws to animate (?ticket=, long-poll with ?since=)
//
// extra holds in-memory files served next to the page (sounds and
// backgrounds).
func Handler(h *Hub, l *Lobby, rx *Reactions, page fs.FS, extra map[string][]byte) http.Handler {
	mux := http.NewServeMux()
	files := http.FileServerFS(page)

	mux.HandleFunc("GET /s/{token}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		f := r.PathValue("file")
		if f == "config" {
			ice := h.ICE()
			writeJSON(w, http.StatusOK, map[string]any{"iceServers": ice.Servers, "emojis": Emojis, "sounds": rx.Files()})
			return
		}
		if b, ok := extra[f]; ok {
			w.Header().Set("Cache-Control", "private, max-age=86400")
			http.ServeContent(w, r, f, time.Time{}, bytes.NewReader(b))
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + f
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer") // keep the link out of other sites' logs
		files.ServeHTTP(w, r2)
	})
	mux.HandleFunc("GET /s/{token}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
	})

	mux.HandleFunc("POST /s/{token}/join", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		var body struct{ Name string }
		json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body)
		t, err := l.Join(body.Name, clientIP(r))
		if err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"ticket": t})
	})
	mux.HandleFunc("GET /s/{token}/join/{ticket}", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		// Answers when the state leaves ?while= (default pending), or after 25 s,
		// under Cloudflare's 100 s request limit; the page simply asks again.
		while := r.URL.Query().Get("while")
		if while == "" {
			while = Pending
		}
		state := l.Wait(r.PathValue("ticket"), while, 25*time.Second)
		writeJSON(w, http.StatusOK, map[string]string{"state": state})
	})
	// Only people currently watching may see who else is.
	mux.HandleFunc("GET /s/{token}/watchers", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		me, ok := l.Viewer(r.URL.Query().Get("ticket"))
		if !ok {
			http.Error(w, "not watching", http.StatusForbidden)
			return
		}
		since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		if err != nil {
			since = ^uint64(0) // no version yet: answer right away
		}
		ver := h.WaitWatchers(since, 25*time.Second)
		type watcher struct {
			Name  string `json:"name"`
			You   bool   `json:"you"`
			Since int64  `json:"since"` // unix ms, tells joins apart
		}
		list := []watcher{}
		for _, v := range h.Viewers() {
			list = append(list, watcher{v.Name, v.ID == me, v.Since.UnixMilli()})
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": ver, "watchers": list})
	})
	// Only people watching may throw, or see throws.
	mux.HandleFunc("POST /s/{token}/throw", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		var body struct{ Ticket, Emoji string }
		json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body)
		me, ok := l.Viewer(body.Ticket)
		if !ok {
			http.Error(w, "not watching", http.StatusForbidden)
			return
		}
		name := "A friend"
		for _, v := range h.Viewers() {
			if v.ID == me {
				name = v.Name
			}
		}
		switch _, err := rx.Add(body.Ticket, name, body.Emoji); err {
		case nil:
			w.WriteHeader(http.StatusNoContent)
		case ErrThrowRate:
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /s/{token}/throws", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		if _, ok := l.Viewer(r.URL.Query().Get("ticket")); !ok {
			http.Error(w, "not watching", http.StatusForbidden)
			return
		}
		since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		if err != nil { // just joined: start from now, nothing to replay
			writeJSON(w, http.StatusOK, map[string]any{"seq": rx.Seq(), "throws": []Throw{}})
			return
		}
		throws, seq := rx.Since(since, 25*time.Second)
		writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "throws": throws})
	})
	mux.HandleFunc("DELETE /s/{token}/join/{ticket}", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		l.Cancel(r.PathValue("ticket"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /s/{token}/whep", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		ticket := r.Header.Get("X-Ticket")
		who, err := l.Use(ticket)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		offer, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			l.Release(ticket)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, answer, err := h.Answer(string(offer), who.Name)
		if err != nil {
			log.Printf("whep: %v", err)
			l.Release(ticket)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		l.Attach(ticket, id)
		log.Printf("viewer %s (%s) joining from %s", id, who.Name, who.IP)
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", r.URL.Path+"/"+id)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, answer)
	})
	mux.HandleFunc("DELETE /s/{token}/whep/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		if !h.Remove(r.PathValue("id")) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { expired(w) })
	return mux
}

// validToken answers with the "link expired" page unless the request is for
// the current share session.
func validToken(w http.ResponseWriter, r *http.Request, l *Lobby) bool {
	t := l.Token()
	if t == "" || r.PathValue("token") != t {
		expired(w)
		return false
	}
	return true
}

func expired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Movie night</title><body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#000;color:#f2f2f2;font:16px/1.5 system-ui,sans-serif;text-align:center;padding:16px;box-sizing:border-box">
<div><h1 style="margin:0 0 8px">Movie night</h1><p style="color:#a0a0a0;margin:0">This link isn't active right now.<br>Ask the host to start sharing and send you a new link.</p></div>`)
}

// clientIP is the viewer's address; behind the tunnel every request comes
// from loopback, so trust Cloudflare's header only then.
func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return cf
		}
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
