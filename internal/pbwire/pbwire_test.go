package pbwire

import "testing"

func varintBytes(v int) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func field(num int, data []byte) []byte {
	out := append(varintBytes(num<<3|2), varintBytes(len(data))...)
	return append(out, data...)
}

func TestTextIntAndChildAtDepth(t *testing.T) {
	inner := append(field(2, []byte("hello")), 0x08, 0x2a) // field 2 text, field 1 varint 42
	msg := append(field(19, inner), field(20, field(1, []byte("reply")))...)
	if got := Text(msg, 19, 2); got != "hello" {
		t.Fatalf("19.2 = %q", got)
	}
	if v, ok := Int(msg, 19, 1); !ok || v != 42 {
		t.Fatalf("19.1 = %d %v", v, ok)
	}
	if got := Text(msg, 20, 1); got != "reply" {
		t.Fatalf("20.1 = %q", got)
	}
	if got := Text(msg, 21, 1); got != "" {
		t.Fatalf("missing path = %q", got)
	}
	uri := field(1, field(1, []byte("file:///tmp/x")))
	if got := FindText(uri, func(s string) bool { return len(s) > 7 && s[:7] == "file://" }, 3); got != "file:///tmp/x" {
		t.Fatalf("FindText = %q", got)
	}
}

func TestTruncatedInputDoesNotPanic(t *testing.T) {
	for _, b := range [][]byte{{0x12}, {0x12, 0x05, 'a'}, {0x08}, {0xff, 0xff}, nil} {
		if _, err := Parse(b); err == nil && len(b) > 0 {
			t.Errorf("%v parsed without error", b)
		}
		_ = Text(b, 1, 2)
	}
}
