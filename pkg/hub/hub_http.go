package hub

import (
	"net/http"
	"strings"
)

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 2 &&
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		h.grpcServer.ServeHTTP(w, r)
	} else {
		h.mux.ServeHTTP(w, r)
	}
}

func (h *Hub) handleHeathz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}
