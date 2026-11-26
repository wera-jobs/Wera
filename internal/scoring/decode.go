package scoring

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// maxRepairs bounds how many stray closing braces DecodeJSON removes.
const maxRepairs = 3

// DecodeJSON reads a model's JSON reply into v. Models slip in ways that
// are plain to repair, and a whole reply is lost to each slip otherwise
// (on 2026-11-10 7 of 15 resume tailorings failed this way):
//   - a code fence around the JSON, or a sentence before or after it
//     (even one with brackets of its own);
//   - an object closed too early, its last keys left after the brace:
//     {"bullets": {...}}, "notes": [...]}
//
// Only these are repaired; anything else is an error, as before.
func DecodeJSON(reply string, v any) error {
	s := StripFences(reply)
	err := json.Unmarshal([]byte(s), v)
	if err == nil {
		return nil
	}
	// Try each opening bracket in turn: prose before the JSON can hold one
	// of its own ("Results [3 jobs]: {...}").
	firstErr := error(nil)
	for range maxStarts {
		i := strings.IndexAny(s, "{[")
		if i < 0 {
			break
		}
		s = s[i:]
		e := decodeRepaired(s, v)
		if e == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = e
		}
		s = s[1:]
	}
	if firstErr == nil {
		return err
	}
	return firstErr
}

// maxStarts bounds how many opening brackets DecodeJSON tries.
const maxStarts = 4

// decodeRepaired decodes the JSON value s starts with, dropping up to
// maxRepairs braces that close an object too early.
func decodeRepaired(s string, v any) error {
	firstErr := error(nil)
	for range maxRepairs + 1 {
		dec := json.NewDecoder(strings.NewReader(s))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return firstErr
		}
		rest := strings.TrimSpace(s[dec.InputOffset():])
		// The keys left after an early brace belong in the object: drop the brace.
		if strings.HasPrefix(rest, ",") && bytes.HasSuffix(bytes.TrimSpace(raw), []byte("}")) {
			end := int(dec.InputOffset())
			brace := strings.LastIndexByte(s[:end], '}')
			s = s[:brace] + s[brace+1:]
			continue
		}
		// A whole value, maybe with a sentence after it.
		return json.Unmarshal(raw, v)
	}
	return errors.New("the reply's JSON is broken in too many places")
}
