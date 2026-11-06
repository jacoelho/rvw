package server

import "net/http"

// HandlerForTest returns the handler Run would serve for a listener bound to addr.
func (s *Server) HandlerForTest(addr string) http.Handler { return s.handler(addr) }
