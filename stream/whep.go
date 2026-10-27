package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/websocket"
)

// Handler serves everything a viewer needs, all under /s/<token>/ so that a
// link stops working when the share session ends:
//
//	GET    /s/{token}/                 viewer page (and its css/js)
//	GET    /s/{token}/config           STUN/TURN servers, the host's name and throwable emojis
//	POST   /s/{token}/join             {name, host?} → {ticket}; host is the sharer's own key
//	GET    /s/{token}/join/{ticket}    long-poll the sharer's answer (?while=watching: a kick)
//	DELETE /s/{token}/join/{ticket}    withdraw the request
//	POST   /s/{token}/whep             SDP offer (X-Ticket header) → answer
//	DELETE /s/{token}/whep/{id}        leave
//	GET    /s/{token}/cast?ticket=     WebSocket: the stream as MP4 fragments, when no direct path exists
//	GET    /s/{token}/watchers         who's watching (?ticket=, long-poll with ?since=)
//	POST   /s/{token}/throw            {ticket, emoji}: throw it at everyone's screen
//	GET    /s/{token}/throws           throws to animate (?ticket=, long-poll with ?since=)
//	POST   /s/{token}/report           the browser's view of a failed connection, for the log
//
// extra holds in-memory files served next to the page (sounds and
// backgrounds).
func Handler(h *Hub, l *Lobby, rx *Reactions, host string, page fs.FS, extra map[string][]byte) http.Handler {
	mux := http.NewServeMux()
	files := http.FileServerFS(page)

	mux.HandleFunc("GET /s/{token}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		f := r.PathValue("file")
		if f == "config" {
			ice := h.ICE()
			writeJSON(w, http.StatusOK, map[string]any{"iceServers": ice.Servers, "host": host, "emojis": Emojis, "sounds": rx.Files()})
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
		var body struct{ Name, Host string }
		json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body)
		if t, ok := l.JoinHost(body.Name, clientIP(r), body.Host); ok {
			log.Printf("%s (the host) joined their own stream", cleanName(body.Name))
			writeJSON(w, http.StatusCreated, map[string]string{"ticket": t})
			return
		}
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
	// When a connection fails, the page sends what its browser saw, so the
	// sharer's log shows both ends (friends on phones can't send logs).
	var reportMu sync.Mutex
	reports := map[string]int{} // per ticket
	mux.HandleFunc("POST /s/{token}/report", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		var body struct {
			Ticket, Reason, Browser, Network, State string
			GatherMs                                int
			Gathered                                bool
			Local, Pairs                            []string
		}
		json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body)
		name, ok := l.Name(body.Ticket)
		reportMu.Lock()
		if len(reports) > 1000 {
			clear(reports)
		}
		reports[body.Ticket]++
		tooMany := reports[body.Ticket] > 5
		reportMu.Unlock()
		if !ok || tooMany {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		gathered := "all found"
		if !body.Gathered {
			gathered = "still looking when the offer went out"
		}
		log.Printf("browser report from %s: %s (%s); %s; network: %s; addresses gathered in %d ms, %s",
			name, clean(body.Reason), clean(body.State), clean(body.Browser), clean(body.Network), body.GatherMs, gathered)
		log.Printf("browser report from %s: its addresses: %s", name, cleanList(body.Local))
		log.Printf("browser report from %s: paths it tried: %s", name, cleanList(body.Pairs))
		w.WriteHeader(http.StatusNoContent)
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
	// Friends no direct path reaches (strict NAT on both sides) get the
	// stream through the link instead: slower, but it always gets through.
	mux.HandleFunc("GET /s/{token}/cast", func(w http.ResponseWriter, r *http.Request) {
		if !validToken(w, r, l) {
			return
		}
		ticket := r.URL.Query().Get("ticket")
		who, err := l.Use(ticket)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		started := false
		websocket.Server{Handler: func(ws *websocket.Conn) {
			started = true
			ws.PayloadType = websocket.BinaryFrame
			id, v := h.Watch(who.Name)
			l.Attach(ticket, id)
			log.Printf("viewer %s (%s) watching through the link from %s (no direct path; a bit more delay)", id, who.Name, who.IP)
			go func() {
				io.Copy(io.Discard, ws) // returns when the page goes away
				h.Remove(id)
			}()
			defer h.Remove(id)
			defer ws.Close()
			keepalive := time.NewTicker(20 * time.Second) // idle tunnels drop the connection
			defer keepalive.Stop()
			for {
				var msg []byte
				select {
				case msg = <-v.out:
				case <-keepalive.C:
					msg = []byte{'k'}
				case <-v.done:
					return
				}
				ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if _, err := ws.Write(msg); err != nil {
					return
				}
			}
		}}.ServeHTTP(w, r)
		if !started {
			l.Release(ticket)
		}
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

// clean makes text from a browser safe for one log line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

func cleanList(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	var out []string
	for i, s := range list {
		if i == 12 {
			out = append(out, fmt.Sprintf("and %d more", len(list)-12))
			break
		}
		out = append(out, clean(s))
	}
	return strings.Join(out, "; ")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
