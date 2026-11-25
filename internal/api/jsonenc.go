package api

import "unicode/utf8"

// appendJSONString appends s to dst as a quoted JSON string and returns the
// extended slice.
//
// Why not encoding/json? json.Marshal allocates a new slice on every call and
// goes through reflection. This hand-written encoder appends into a buffer we
// reuse across requests, so the GET hot path allocates no response buffers.
//
// Escaping rules (RFC 8259):
//   - `"` and `\` are backslash-escaped
//   - control characters (< 0x20) become \n, \r, \t or \u00XX
//   - invalid UTF-8 bytes become U+FFFD, the same as encoding/json
//   - U+2028 / U+2029 are escaped, which keeps output safe to embed in JS
func appendJSONString(dst []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"

	dst = append(dst, '"')
	start := 0 // beginning of the current run of bytes that need no escaping

	for i := 0; i < len(s); {
		c := s[i]

		// Fast path: plain ASCII that needs no escaping.
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			// Flush the safe run, then write the escape sequence.
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}

		// Multi-byte UTF-8 sequence.
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1: // invalid byte
			dst = append(dst, s[start:i]...)
			dst = append(dst, `\ufffd`...)
		case r == '\u2028' || r == '\u2029':
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
		default: // valid character, copy it as-is later
			i += size
			continue
		}
		i += size
		start = i
	}

	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
