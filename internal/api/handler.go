// Package api exposes the cache over HTTP using fasthttp.
//
// Endpoints:
//
//	POST /put          {"key":"...","value":"..."}  insert or update (PUT also accepted)
//	GET  /get?key=...                               read a key
//	GET  /health                                    liveness/readiness probe
//	GET  /stats                                     cache counters
//
// Success responses are byte-for-byte stable, so clients and load-testing
// tools can compare them directly. Errors use one JSON envelope:
// {"status":"ERROR","message":"..."}.
package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/valyala/fasthttp"

	"go-kv-cache/internal/cache"
)

// Store is the subset of the cache the handler needs. Depending on an
// interface (instead of *cache.Cache) lets tests plug in a fake store.
type Store interface {
	Get(key string) (string, bool)
	Set(key, value string) bool
	Stats() cache.Stats
}

// Limits bounds the size of incoming data.
type Limits struct {
	MaxKeyBytes   int
	MaxValueBytes int
	MaxBodyBytes  int
}

// Pre-built response bodies. Building them once at startup means the hot
// path never re-serializes constant JSON.
var (
	bodyPutOK       = []byte(`{"status":"OK","message":"Key inserted/updated successfully."}`)
	bodyKeyNotFound = []byte(`{"status":"ERROR","message":"Key not found."}`)
	bodyHealthy     = []byte(`{"status":"healthy"}`)
)

const contentTypeJSON = "application/json"

// bufPool recycles byte slices used to build GET responses, so serving a
// read does not allocate a new buffer each time.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

// Handler routes and serves HTTP requests.
type Handler struct {
	store  Store
	limits Limits
}

// NewHandler creates a Handler backed by store.
func NewHandler(store Store, limits Limits) *Handler {
	return &Handler{store: store, limits: limits}
}

// Serve is the fasthttp.RequestHandler entry point.
//
// A plain switch on the path is the fastest router for a handful of fixed
// routes; `string(ctx.Path())` inside a switch does not allocate because the
// Go compiler optimizes that conversion.
func (h *Handler) Serve(ctx *fasthttp.RequestCtx) {
	switch string(ctx.Path()) {
	case "/get":
		if !ctx.IsGet() && !ctx.IsHead() {
			methodNotAllowed(ctx, "GET, HEAD")
			return
		}
		h.get(ctx)
	case "/put":
		if !ctx.IsPost() && !ctx.IsPut() {
			methodNotAllowed(ctx, "POST, PUT")
			return
		}
		h.put(ctx)
	case "/health":
		if !ctx.IsGet() && !ctx.IsHead() {
			methodNotAllowed(ctx, "GET, HEAD")
			return
		}
		writeJSON(ctx, fasthttp.StatusOK, bodyHealthy)
	case "/stats":
		if !ctx.IsGet() && !ctx.IsHead() {
			methodNotAllowed(ctx, "GET, HEAD")
			return
		}
		h.stats(ctx)
	default:
		writeError(ctx, fasthttp.StatusNotFound, "Unsupported request.")
	}
}

// putRequest is the JSON body of /put. Pointer fields let us tell a missing
// field (nil) apart from an empty string ("").
type putRequest struct {
	Key   *string `json:"key"`
	Value *string `json:"value"`
}

func (h *Handler) put(ctx *fasthttp.RequestCtx) {
	body := ctx.PostBody()
	// fasthttp already enforces MaxRequestBodySize at the connection level;
	// this check keeps the handler safe even if used with another server.
	if len(body) > h.limits.MaxBodyBytes {
		writeError(ctx, fasthttp.StatusRequestEntityTooLarge, "Request body too large.")
		return
	}

	var req putRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(ctx, fasthttp.StatusBadRequest, decodeErrorMessage(err))
		return
	}

	if msg := validateKey(req.Key, h.limits.MaxKeyBytes); msg != "" {
		writeError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}
	if req.Value == nil {
		writeError(ctx, fasthttp.StatusBadRequest, "Value must be a string.")
		return
	}
	if len(*req.Value) > h.limits.MaxValueBytes {
		writeError(ctx, fasthttp.StatusBadRequest,
			"Value must be at most "+strconv.Itoa(h.limits.MaxValueBytes)+" bytes.")
		return
	}

	h.store.Set(*req.Key, *req.Value)
	writeJSON(ctx, fasthttp.StatusOK, bodyPutOK)
}

func (h *Handler) get(ctx *fasthttp.RequestCtx) {
	key := string(ctx.QueryArgs().Peek("key"))
	if msg := validateKey(&key, h.limits.MaxKeyBytes); msg != "" {
		writeError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}

	value, found := h.store.Get(key)
	if !found {
		// A miss is a normal cache outcome, so it is reported in the body
		// with HTTP 200 rather than as an HTTP error.
		writeJSON(ctx, fasthttp.StatusOK, bodyKeyNotFound)
		return
	}

	// Build {"status":"OK","key":...,"value":...} in a pooled buffer.
	bp := bufPool.Get().(*[]byte)
	b := (*bp)[:0] // reuse the pooled buffer's memory, starting empty
	b = append(b, `{"status":"OK","key":`...)
	b = appendJSONString(b, key)
	b = append(b, `,"value":`...)
	b = appendJSONString(b, value)
	b = append(b, '}')

	writeJSON(ctx, fasthttp.StatusOK, b) // SetBody copies b, so reuse is safe
	*bp = b
	bufPool.Put(bp)
}

func (h *Handler) stats(ctx *fasthttp.RequestCtx) {
	body, err := json.Marshal(struct {
		Status string      `json:"status"`
		Cache  cache.Stats `json:"cache"`
	}{Status: "OK", Cache: h.store.Stats()})
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "Internal server error.")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, body)
}

// validateKey returns an error message, or "" when the key is valid.
func validateKey(key *string, maxBytes int) string {
	if key == nil || *key == "" {
		return "Key parameter is required."
	}
	if len(*key) > maxBytes { // len() of a Go string counts bytes
		return "Key must be at most " + strconv.Itoa(maxBytes) + " bytes."
	}
	return ""
}

// decodeErrorMessage turns a json.Unmarshal error into a client-facing
// message without leaking Go type names.
func decodeErrorMessage(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		switch typeErr.Field {
		case "key":
			return "Key must be a string."
		case "value":
			return "Value must be a string."
		}
		return "Request body must be a JSON object with \"key\" and \"value\"."
	}
	return "Malformed JSON body."
}

func writeJSON(ctx *fasthttp.RequestCtx, status int, body []byte) {
	ctx.SetStatusCode(status)
	ctx.SetContentType(contentTypeJSON)
	ctx.SetBody(body)
}

func writeError(ctx *fasthttp.RequestCtx, status int, message string) {
	b := append([]byte(`{"status":"ERROR","message":`), appendJSONString(nil, message)...)
	writeJSON(ctx, status, append(b, '}'))
}

func methodNotAllowed(ctx *fasthttp.RequestCtx, allowed string) {
	ctx.Response.Header.Set(fasthttp.HeaderAllow, allowed)
	writeError(ctx, fasthttp.StatusMethodNotAllowed, "Method not allowed.")
}
