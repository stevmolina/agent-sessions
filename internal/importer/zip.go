// Package importer reads chat app data exports (ChatGPT, Claude.ai, Grok) and
// turns each conversation into a source.Imported. A zip is untrusted input: it
// is read in memory, entry names are never used as paths, and the bytes read
// are capped per entry and in total.
package importer

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/usuario/sessions/internal/source"
)

// Limits caps how much one zip may decompress. Exports with years of history
// run to a few hundred megabytes of JSON, so the defaults leave room. JSON is
// streamed, so memory follows the largest single conversation, not the file.
type Limits struct {
	MaxEntry int64 // bytes read from one entry
	MaxTotal int64 // bytes read from all entries of one zip
}

var DefaultLimits = Limits{MaxEntry: 512 << 20, MaxTotal: 2 << 30}

// Result is what one zip held.
type Result struct {
	Conversations []source.Imported
	// Skipped counts conversations that could not be read or had no text,
	// by source. Items whose format could not be told are under "unknown".
	Skipped map[string]int
}

// collector receives conversations as they are parsed.
type collector struct {
	emit    func(source.Imported) error
	skipped map[string]int
}

func (c *collector) skip(src string) {
	if c.skipped == nil {
		c.skipped = map[string]int{}
	}
	c.skipped[src]++
}

// ErrNoExport means the zip held none of the files a supported export has.
var ErrNoExport = errors.New("no ChatGPT, Claude.ai or Grok conversations found")

var conversationsFile = regexp.MustCompile(`^conversations(-\d+)?\.json$`)

func entryKind(name string) string {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	switch {
	case conversationsFile.MatchString(base):
		return "conversations"
	case strings.HasPrefix(base, "prod-grok-backend") && strings.HasSuffix(base, ".json"):
		return "grok"
	}
	return ""
}

// ReadZip parses every conversation in one export zip and returns them all.
// ImportZip stores them as they are parsed instead.
func ReadZip(file string, lim Limits) (Result, error) {
	zr, err := openZip(file)
	if err != nil {
		return Result{}, err
	}
	defer zr.Close()
	return collect(&zr.Reader, lim)
}

// ReadZipFrom parses an export zip held in a ReaderAt (tests, or a download).
func ReadZipFrom(r io.ReaderAt, size int64, lim Limits) (Result, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		return Result{}, errors.New("not a readable zip")
	}
	return collect(zr, lim)
}

func openZip(file string) (*zip.ReadCloser, error) {
	zr, err := zip.OpenReader(file)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		return nil, fmt.Errorf("%s: not a readable zip", file)
	}
	return zr, nil
}

func collect(zr *zip.Reader, lim Limits) (Result, error) {
	var res Result
	c := &collector{emit: func(conv source.Imported) error {
		res.Conversations = append(res.Conversations, conv)
		return nil
	}}
	err := readZip(zr, lim, c)
	res.Skipped = c.skipped
	return res, err
}

func readZip(zr *zip.Reader, lim Limits, c *collector) error {
	b := &budget{max: lim.MaxTotal}
	found := false
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		kind := entryKind(f.Name)
		if kind == "" {
			continue
		}
		found = true
		if lim.MaxEntry > 0 && f.UncompressedSize64 > uint64(lim.MaxEntry) {
			return fmt.Errorf("%s is larger than the %d byte limit", path.Base(f.Name), lim.MaxEntry)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%s: %w", path.Base(f.Name), err)
		}
		r := &limitedReader{r: rc, max: lim.MaxEntry, name: path.Base(f.Name), budget: b}
		switch kind {
		case "conversations":
			err = parseConversations(r, c)
		case "grok":
			err = parseGrok(r, c)
		}
		rc.Close()
		if err != nil {
			var tooBig *limitError
			if errors.As(err, &tooBig) {
				return tooBig
			}
			return fmt.Errorf("%s: %w", path.Base(f.Name), err)
		}
	}
	if !found {
		return ErrNoExport
	}
	return nil
}

type budget struct{ used, max int64 }

type limitError struct{ msg string }

func (e *limitError) Error() string { return e.msg }

// limitedReader fails, instead of truncating silently, once an entry or the
// whole zip passes its limit. The sizes in the zip headers are not trusted.
// It never hands out a byte past a limit, and the failure sticks, so a decoder
// that has buffered enough to finish still cannot accept an oversized file.
type limitedReader struct {
	r         io.Reader
	used, max int64
	name      string
	budget    *budget
	err       error
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	// Read one byte past the tighter limit, to tell "exactly at" from "over".
	room := int64(-1)
	if l.max > 0 {
		room = l.max - l.used
	}
	if l.budget.max > 0 && (room < 0 || l.budget.max-l.budget.used < room) {
		room = l.budget.max - l.budget.used
	}
	if room >= 0 && int64(len(p)) > room+1 {
		p = p[:room+1]
	}
	n, err := l.r.Read(p)
	if room >= 0 && int64(n) > room {
		n = int(room)
		if l.max > 0 && l.used+int64(n) >= l.max {
			l.err = &limitError{msg: l.name + " is larger than the per-file limit"}
		} else {
			l.err = &limitError{msg: "the export is larger than the total limit"}
		}
	}
	l.used += int64(n)
	l.budget.used += int64(n)
	if l.err != nil {
		return n, l.err
	}
	return n, err
}
