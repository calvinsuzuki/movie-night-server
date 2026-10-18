package stream

import (
	"io"
	"log"
	"net/http"
)

// Register adds WHEP-style signaling: POST an SDP offer, get an SDP answer and
// a resource URL; DELETE that URL to leave.
func (h *Hub) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /whep", func(w http.ResponseWriter, r *http.Request) {
		offer, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, answer, err := h.Answer(string(offer))
		if err != nil {
			log.Printf("whep: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("viewer %s joining from %s", id, r.RemoteAddr)
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", "/whep/"+id)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, answer)
	})
	mux.HandleFunc("DELETE /whep/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !h.Remove(r.PathValue("id")) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
