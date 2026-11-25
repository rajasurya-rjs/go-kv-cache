package api

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"

	"go-kv-cache/internal/cache"
)

var testLimits = Limits{MaxKeyBytes: 16, MaxValueBytes: 16, MaxBodyBytes: 256}

// newTestHandler returns a handler backed by a real, small cache.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	c, err := cache.New(100, 4)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(c, testLimits)
}

// do calls the handler directly, with no network involved, and returns the
// response. This is the fastest way to unit test fasthttp handlers.
func do(h *Handler, method, uri, body string) *fasthttp.Response {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	if body != "" {
		req.SetBodyString(body)
	}

	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	h.Serve(&ctx)

	var resp fasthttp.Response
	ctx.Response.CopyTo(&resp)
	return &resp
}

// expect asserts status code and exact body.
func expect(t *testing.T, resp *fasthttp.Response, status int, body string) {
	t.Helper()
	if resp.StatusCode() != status {
		t.Errorf("status = %d; want %d (body %s)", resp.StatusCode(), status, resp.Body())
	}
	if string(resp.Body()) != body {
		t.Errorf("body = %s\n       want %s", resp.Body(), body)
	}
}

const (
	okPut    = `{"status":"OK","message":"Key inserted/updated successfully."}`
	notFound = `{"status":"ERROR","message":"Key not found."}`
)

func TestHealth(t *testing.T) {
	h := newTestHandler(t)
	resp := do(h, "GET", "/health", "")
	expect(t, resp, 200, `{"status":"healthy"}`)
	if ct := string(resp.Header.ContentType()); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestPutThenGet(t *testing.T) {
	h := newTestHandler(t)
	expect(t, do(h, "POST", "/put", `{"key":"name","value":"fast"}`), 200, okPut)
	expect(t, do(h, "GET", "/get?key=name", ""), 200, `{"status":"OK","key":"name","value":"fast"}`)
}

func TestPutAcceptsPUTMethod(t *testing.T) {
	h := newTestHandler(t)
	expect(t, do(h, "PUT", "/put", `{"key":"a","value":"1"}`), 200, okPut)
	expect(t, do(h, "GET", "/get?key=a", ""), 200, `{"status":"OK","key":"a","value":"1"}`)
}

func TestUpdateOverwrites(t *testing.T) {
	h := newTestHandler(t)
	do(h, "POST", "/put", `{"key":"k","value":"v1"}`)
	do(h, "POST", "/put", `{"key":"k","value":"v2"}`)
	expect(t, do(h, "GET", "/get?key=k", ""), 200, `{"status":"OK","key":"k","value":"v2"}`)
}

func TestGetMissingKey(t *testing.T) {
	expect(t, do(newTestHandler(t), "GET", "/get?key=ghost", ""), 200, notFound)
}

func TestEmptyValueRoundTrip(t *testing.T) {
	h := newTestHandler(t)
	expect(t, do(h, "POST", "/put", `{"key":"e","value":""}`), 200, okPut)
	expect(t, do(h, "GET", "/get?key=e", ""), 200, `{"status":"OK","key":"e","value":""}`)
}

func TestSpecialCharactersAreEscaped(t *testing.T) {
	h := newTestHandler(t)
	key := `a "q" & b`
	do(h, "POST", "/put", `{"key":"a \"q\" & b","value":"line\nbreak"}`)

	resp := do(h, "GET", "/get?key="+url.QueryEscape(key), "")
	var got map[string]string
	if err := json.Unmarshal(resp.Body(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %s", resp.Body())
	}
	if got["key"] != key || got["value"] != "line\nbreak" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestGetValidation(t *testing.T) {
	h := newTestHandler(t)
	expect(t, do(h, "GET", "/get", ""), 400, `{"status":"ERROR","message":"Key parameter is required."}`)
	expect(t, do(h, "GET", "/get?key=", ""), 400, `{"status":"ERROR","message":"Key parameter is required."}`)
	expect(t, do(h, "GET", "/get?key="+strings.Repeat("k", 17), ""), 400,
		`{"status":"ERROR","message":"Key must be at most 16 bytes."}`)
}

func TestPutValidation(t *testing.T) {
	h := newTestHandler(t)
	tests := []struct{ name, body, message string }{
		{"malformed", `{"key":`, "Malformed JSON body."},
		{"empty body", ``, "Malformed JSON body."},
		{"array", `["a","b"]`, `Request body must be a JSON object with \"key\" and \"value\".`},
		{"missing key", `{"value":"v"}`, "Key parameter is required."},
		{"empty key", `{"key":"","value":"v"}`, "Key parameter is required."},
		{"null body", `null`, "Key parameter is required."},
		{"number key", `{"key":1,"value":"v"}`, "Key must be a string."},
		{"missing value", `{"key":"k"}`, "Value must be a string."},
		{"number value", `{"key":"k","value":5}`, "Value must be a string."},
		{"long key", `{"key":"` + strings.Repeat("k", 17) + `","value":"v"}`, "Key must be at most 16 bytes."},
		{"long value", `{"key":"k","value":"` + strings.Repeat("v", 17) + `"}`, "Value must be at most 16 bytes."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expect(t, do(h, "POST", "/put", tt.body), 400, `{"status":"ERROR","message":"`+tt.message+`"}`)
		})
	}
}

func TestKeyAndValueAtExactLimit(t *testing.T) {
	h := newTestHandler(t)
	k, v := strings.Repeat("k", 16), strings.Repeat("v", 16)
	expect(t, do(h, "POST", "/put", `{"key":"`+k+`","value":"`+v+`"}`), 200, okPut)
}

func TestOversizedBodyInHandler(t *testing.T) {
	h := newTestHandler(t)
	body := `{"key":"k","value":"` + strings.Repeat("v", 300) + `"}`
	expect(t, do(h, "POST", "/put", body), 413, `{"status":"ERROR","message":"Request body too large."}`)
}

func TestRouting(t *testing.T) {
	h := newTestHandler(t)
	expect(t, do(h, "GET", "/nope", ""), 404, `{"status":"ERROR","message":"Unsupported request."}`)

	wrongMethods := []struct{ method, path, allow string }{
		{"GET", "/put", "POST, PUT"},
		{"POST", "/get", "GET, HEAD"},
		{"DELETE", "/health", "GET, HEAD"},
		{"POST", "/stats", "GET, HEAD"},
	}
	for _, tt := range wrongMethods {
		resp := do(h, tt.method, tt.path, "")
		expect(t, resp, 405, `{"status":"ERROR","message":"Method not allowed."}`)
		if got := string(resp.Header.Peek("Allow")); got != tt.allow {
			t.Errorf("%s %s Allow = %q; want %q", tt.method, tt.path, got, tt.allow)
		}
	}
}

func TestStats(t *testing.T) {
	h := newTestHandler(t)
	do(h, "POST", "/put", `{"key":"a","value":"1"}`)
	do(h, "GET", "/get?key=a", "")
	do(h, "GET", "/get?key=zzz", "")

	resp := do(h, "GET", "/stats", "")
	var got struct {
		Status string      `json:"status"`
		Cache  cache.Stats `json:"cache"`
	}
	if err := json.Unmarshal(resp.Body(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "OK" || got.Cache.Entries != 1 || got.Cache.Hits != 1 || got.Cache.Misses != 1 {
		t.Fatalf("unexpected stats: %+v", got)
	}
}

// Benchmarks measure the full handler path without network overhead.
func BenchmarkHandlerGet(b *testing.B) {
	c, _ := cache.New(1000, 16)
	h := NewHandler(c, Limits{MaxKeyBytes: 256, MaxValueBytes: 256, MaxBodyBytes: 4096})
	c.Set("bench", "value")

	var req fasthttp.Request
	req.SetRequestURI("/get?key=bench")
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)

	b.ReportAllocs()
	for b.Loop() {
		ctx.Response.Reset()
		h.Serve(&ctx)
	}
}

func BenchmarkHandlerPut(b *testing.B) {
	c, _ := cache.New(1000, 16)
	h := NewHandler(c, Limits{MaxKeyBytes: 256, MaxValueBytes: 256, MaxBodyBytes: 4096})

	var req fasthttp.Request
	req.Header.SetMethod("POST")
	req.SetRequestURI("/put")
	req.SetBodyString(`{"key":"bench","value":"value"}`)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)

	b.ReportAllocs()
	for b.Loop() {
		ctx.Response.Reset()
		h.Serve(&ctx)
	}
}
