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
//   - a code fence around the JSON, or a sentence before or after it;
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
	i := strings.IndexAny(s, "{[")
	if i < 0 {
		return err
	}
	s = s[i:]
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
