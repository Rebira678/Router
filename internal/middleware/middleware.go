package middleware

import "net/http"

// Middleware is the standard signature for HTTP middleware in Go.
// It takes the next handler in the chain and returns a wrapped handler.
type Middleware func(http.Handler) http.Handler

// Chain takes a base handler and wraps it with the provided middlewares.
// The middlewares are applied in reverse order so that the first middleware
// in the list is executed first (i.e. it is the outermost wrapper).
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
