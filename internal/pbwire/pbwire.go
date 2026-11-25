// Package pbwire reads the Protocol Buffers wire format without a schema. It is
// enough to pull text and numbers out of the blobs that Antigravity stores in
// its conversation databases, where no .proto file is published.
package pbwire

import (
	"errors"
	"unicode/utf8"
)

// Field is one decoded field. Varint holds wire type 0; Bytes holds wire type 2.
type Field struct {
	Num    int
	Wire   int
	Varint uint64
	Bytes  []byte
}

var errTruncated = errors.New("pbwire: truncated message")

// Parse decodes one message level. A malformed message returns an error and the
// fields read so far.
func Parse(b []byte) ([]Field, error) {
	var out []Field
	for i := 0; i < len(b); {
		key, n := varint(b[i:])
		if n == 0 {
			return out, errTruncated
		}
		i += n
		f := Field{Num: int(key >> 3), Wire: int(key & 7)}
		switch f.Wire {
		case 0:
			v, n := varint(b[i:])
			if n == 0 {
				return out, errTruncated
			}
			i += n
			f.Varint = v
		case 1:
			if i+8 > len(b) {
				return out, errTruncated
			}
			i += 8
		case 5:
			if i+4 > len(b) {
				return out, errTruncated
			}
			i += 4
		case 2:
			l, n := varint(b[i:])
			if n == 0 || l > uint64(len(b)-i-n) {
				return out, errTruncated
			}
			i += n
			f.Bytes = b[i : i+int(l)]
			i += int(l)
		default:
			return out, errors.New("pbwire: unsupported wire type")
		}
		out = append(out, f)
	}
	return out, nil
}

func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// Child returns the first submessage at the given field path, or nil.
func Child(b []byte, path ...int) []byte {
	for _, num := range path {
		fields, _ := Parse(b)
		found := false
		for _, f := range fields {
			if f.Num == num && f.Wire == 2 {
				b, found = f.Bytes, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return b
}

// Text returns the first field at the path that holds valid UTF-8 text.
func Text(b []byte, path ...int) string {
	if len(path) == 0 {
		return ""
	}
	parent := Child(b, path[:len(path)-1]...)
	if parent == nil && len(path) > 1 {
		return ""
	}
	if len(path) == 1 {
		parent = b
	}
	fields, _ := Parse(parent)
	for _, f := range fields {
		if f.Num == path[len(path)-1] && f.Wire == 2 && utf8.Valid(f.Bytes) && len(f.Bytes) > 0 {
			return string(f.Bytes)
		}
	}
	return ""
}

// Int returns the first varint at the path, and whether it exists.
func Int(b []byte, path ...int) (uint64, bool) {
	if len(path) == 0 {
		return 0, false
	}
	parent := b
	if len(path) > 1 {
		parent = Child(b, path[:len(path)-1]...)
		if parent == nil {
			return 0, false
		}
	}
	fields, _ := Parse(parent)
	for _, f := range fields {
		if f.Num == path[len(path)-1] && f.Wire == 0 {
			return f.Varint, true
		}
	}
	return 0, false
}

// FindText walks every nested message and returns the first valid UTF-8 string
// that satisfies ok. Used for values at unknown depth, such as a folder URI.
func FindText(b []byte, ok func(string) bool, depth int) string {
	fields, _ := Parse(b)
	for _, f := range fields {
		if f.Wire != 2 || len(f.Bytes) == 0 {
			continue
		}
		if utf8.Valid(f.Bytes) && ok(string(f.Bytes)) {
			return string(f.Bytes)
		}
		if depth > 0 {
			if s := FindText(f.Bytes, ok, depth-1); s != "" {
				return s
			}
		}
	}
	return ""
}
