package api

import (
	"errors"

	"github.com/valyala/fasthttp"

	"go-kv-cache/internal/config"
)

// NewServer wires a Handler into a tuned fasthttp.Server.
//
// fasthttp is used instead of net/http because it reuses request/response
// objects across requests instead of allocating new ones, which gives much
// lower latency and GC pressure for small, high-rate requests like ours.
func NewServer(cfg config.Config, store Store) *fasthttp.Server {
	h := NewHandler(store, Limits{
		MaxKeyBytes:   cfg.MaxKeyBytes,
		MaxValueBytes: cfg.MaxValueBytes,
		MaxBodyBytes:  cfg.MaxBodyBytes,
	})

	return &fasthttp.Server{
		Handler: h.Serve,

		// Don't advertise the server software.
		NoDefaultServerHeader: true,

		// Bound slow clients so they cannot hold connections forever.
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		// Keep idle connections open for reuse (HTTP keep-alive), since
		// reconnecting costs far more than a cache lookup.
		IdleTimeout: cfg.IdleTimeout,

		// Reject oversized bodies before they are buffered in memory.
		MaxRequestBodySize: cfg.MaxBodyBytes,

		// During shutdown, add "Connection: close" so keep-alive clients
		// disconnect cleanly instead of reusing a closing socket.
		CloseOnShutdown: true,

		// Errors raised by fasthttp itself (before our handler runs) get
		// the same JSON envelope as every other error.
		ErrorHandler: serverError,
	}
}

// serverError answers requests that fasthttp rejected while parsing them,
// such as bodies over MaxRequestBodySize or malformed HTTP.
func serverError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, fasthttp.ErrBodyTooLarge) {
		writeError(ctx, fasthttp.StatusRequestEntityTooLarge, "Request body too large.")
		return
	}
	writeError(ctx, fasthttp.StatusBadRequest, "Bad request.")
}
