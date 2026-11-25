package api

import (
	"encoding/json"
	"testing"
)

func TestAppendJSONString(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", `""`},
		{"hello", `"hello"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak\ttab\rcr", `"line\nbreak\ttab\rcr"`},
		{"\x00\x1f", `"\u0000\u001f"`},
		{"héllo 🚀", `"héllo 🚀"`},                 // valid UTF-8 is copied as-is
		{"bad\xffbyte", `"bad\ufffdbyte"`},       // invalid UTF-8 is replaced
		{"a\u2028b\u2029c", `"a\u2028b\u2029c"`}, // JS line separators are escaped
		{"a&b<c>", `"a&b<c>"`},                   // valid JSON, no HTML escaping needed
	}
	for _, tt := range tests {
		if got := string(appendJSONString(nil, tt.in)); got != tt.want {
			t.Errorf("appendJSONString(%q) = %s; want %s", tt.in, got, tt.want)
		}
	}
}

func TestAppendJSONStringAppends(t *testing.T) {
	got := appendJSONString([]byte("prefix:"), "x")
	if string(got) != `prefix:"x"` {
		t.Fatalf("got %s", got)
	}
}

// FuzzAppendJSONString checks, for any random input, that our encoder
// produces valid JSON that decodes to exactly what encoding/json would give.
// `go test` runs the seed corpus; `go test -fuzz=FuzzAppendJSONString`
// generates new inputs continuously.
func FuzzAppendJSONString(f *testing.F) {
	for _, seed := range []string{"", "plain", `"\`, "\x00\n", "é🚀", "\xff\xfe", "\u2028"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := appendJSONString(nil, s)

		var got string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("invalid JSON %q for input %q: %v", out, s, err)
		}

		std, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := json.Unmarshal(std, &want); err != nil {
			t.Fatal(err)
		}

		if got != want {
			t.Fatalf("decoded %q; encoding/json gives %q (input %q)", got, want, s)
		}
	})
}

func BenchmarkAppendJSONString(b *testing.B) {
	buf := make([]byte, 0, 512)
	s := "a typical cache value with some text and a \"quote\""
	b.ReportAllocs()
	for b.Loop() {
		buf = appendJSONString(buf[:0], s)
	}
}
