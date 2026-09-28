// Package clean redacts secrets from session text before it is indexed.
// Raw transcript files are never written.
package clean

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/rs/zerolog"
	"github.com/usuario/sessions/internal/model"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/logging"
	"github.com/zricethezav/gitleaks/v8/report"
)

const placeholderPrefix = "[REDACTED:"

// ErrFailed means the text could not be cleaned. Callers must drop the
// session instead of storing it.
var ErrFailed = errors.New("redaction failed")

type replacement struct {
	label string
	value string
}

// Cleaner applies Doppler exact matches and gitleaks rules.
type Cleaner struct {
	repl []replacement
	det  *detect.Detector
	fp   string
}

var (
	detOnce sync.Once
	det     *detect.Detector
	detErr  error
)

func detector() (*detect.Detector, error) {
	detOnce.Do(func() {
		logging.Logger = zerolog.New(io.Discard).Level(zerolog.Disabled)
		det, detErr = detect.NewDetectorDefaultConfig()
	})
	return det, detErr
}

// New builds a cleaner. secrets maps a label (the Doppler name) to the
// raw value. Short and trivial values are ignored so ordinary words survive.
func New(secrets map[string]string) (*Cleaner, error) {
	d, err := detector()
	if err != nil {
		return nil, ErrFailed
	}
	repl := buildReplacements(secrets)
	return &Cleaner{repl: repl, det: d, fp: fingerprint(repl)}, nil
}

// Fingerprint changes when the secret set or the gitleaks rules change.
// It is safe to store. It does not contain secret values.
func (c *Cleaner) Fingerprint() string {
	if c == nil {
		return ""
	}
	return c.fp
}

// Apply redacts title, turns, and body. On error the session must not be stored.
func (c *Cleaner) Apply(s *model.Session) error {
	if c == nil || s == nil {
		return ErrFailed
	}
	title, err := c.Text(s.Title)
	if err != nil {
		return err
	}
	s.Title = title
	for i := range s.Turns {
		text, err := c.Text(s.Turns[i].Text)
		if err != nil {
			return err
		}
		s.Turns[i].Text = text
	}
	if len(s.Turns) == 0 {
		body, err := c.Text(s.Body)
		if err != nil {
			return err
		}
		s.Body = body
		return nil
	}
	parts := make([]string, 0, len(s.Turns))
	for _, t := range s.Turns {
		parts = append(parts, t.Role+": "+t.Text)
	}
	s.Body = strings.Join(parts, "\n\n")
	return nil
}

// Text redacts one string. Exact values are replaced first, then gitleaks matches.
func (c *Cleaner) Text(in string) (string, error) {
	if c == nil || c.det == nil {
		return "", ErrFailed
	}
	if in == "" {
		return "", nil
	}
	out := in
	for _, r := range c.repl {
		out = strings.ReplaceAll(out, r.value, placeholder(r.label))
	}
	for pass := 0; pass < 3; pass++ {
		findings := c.det.DetectString(out)
		if len(findings) == 0 {
			if leftover(out, c.repl) {
				return "", ErrFailed
			}
			return out, nil
		}
		next := redactFindings(out, findings)
		if next == out {
			return "", ErrFailed
		}
		out = next
	}
	if len(c.det.DetectString(out)) > 0 || leftover(out, c.repl) {
		return "", ErrFailed
	}
	return out, nil
}

// Redact is the test seam: exact secrets plus gitleaks rules.
func Redact(text string, secrets map[string]string) (string, error) {
	c, err := New(secrets)
	if err != nil {
		return "", err
	}
	return c.Text(text)
}

func leftover(text string, repl []replacement) bool {
	for _, r := range repl {
		if strings.Contains(text, r.value) {
			return true
		}
	}
	return false
}

func redactFindings(in string, findings []report.Finding) string {
	type hit struct {
		needle string
		label  string
	}
	var hits []hit
	for _, f := range findings {
		label := f.RuleID
		if label == "" {
			label = "pattern"
		}
		if f.Match != "" {
			hits = append(hits, hit{f.Match, label})
		}
		if f.Secret != "" && f.Secret != f.Match {
			hits = append(hits, hit{f.Secret, label})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if len(hits[i].needle) != len(hits[j].needle) {
			return len(hits[i].needle) > len(hits[j].needle)
		}
		return hits[i].label < hits[j].label
	})
	out := in
	for _, h := range hits {
		if len(h.needle) < 8 {
			continue
		}
		out = strings.ReplaceAll(out, h.needle, placeholder(h.label))
	}
	return out
}

func placeholder(label string) string {
	return placeholderPrefix + label + "]"
}

func buildReplacements(secrets map[string]string) []replacement {
	byValue := map[string]string{}
	for label, value := range secrets {
		value = strings.TrimSpace(value)
		if !usable(value) {
			continue
		}
		label = sanitizeLabel(label)
		if prev, ok := byValue[value]; !ok || label < prev {
			byValue[value] = label
		}
	}
	out := make([]replacement, 0, len(byValue))
	for value, label := range byValue {
		out = append(out, replacement{label: label, value: value})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].value) != len(out[j].value) {
			return len(out[i].value) > len(out[j].value)
		}
		return out[i].label < out[j].label
	})
	return out
}

// usable drops values that would chew up ordinary prose.
// Length is in bytes, which matches how the values are matched.
func usable(v string) bool {
	if len(v) < 8 {
		return false
	}
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "null", "none", "on", "off":
		return false
	}
	if allDigits(v) && len(v) < 8 {
		return false
	}
	if allLetters(v) && len(v) < 16 {
		return false
	}
	return true
}

func allDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func allLetters(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func sanitizeLabel(s string) string {
	b := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-' || r == '.' || r == '/':
			return r
		default:
			return '_'
		}
	}, s)
	if b == "" {
		return "secret"
	}
	return b
}

func fingerprint(repl []replacement) string {
	h := sha256.New()
	_, _ = io.WriteString(h, "v1\n")
	sum := sha256.Sum256([]byte(config.DefaultConfig))
	_, _ = h.Write(sum[:])
	for _, r := range repl {
		_, _ = h.Write([]byte(r.label))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.value))
		_, _ = h.Write([]byte{'\n'})
	}
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}
