package scoring

import "testing"

func TestDecodeJSON(t *testing.T) {
	type reply struct {
		Bullets map[string]string `json:"bullets"`
		Notes   []string          `json:"notes"`
	}
	cases := []struct {
		name, in string
		ok       bool
		notes    int
	}{
		{"plain", `{"bullets": {"a": "x"}, "notes": ["n"]}`, true, 1},
		{"fenced", "```json\n{\"bullets\": {}, \"notes\": [\"n\"]}\n```", true, 1},
		{"closed too early", `{"bullets": {"a": "x"}, "skills": {"g": "y"}}, "notes": ["n1", "n2"]}`, true, 2},
		{"closed too early twice", `{"bullets": {"a": "x"}}, "skills": {}}, "notes": ["n"]}`, true, 1},
		{"sentence around it", "Here you go:\n{\"bullets\": {}, \"notes\": [\"n\"]}\nGood luck!", true, 1},
		{"not JSON", `I can't help with that.`, false, 0},
		{"cut off", `{"bullets": {"a": "x"`, false, 0},
	}
	for _, c := range cases {
		var r reply
		err := DecodeJSON(c.in, &r)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
			continue
		}
		if c.ok && len(r.Notes) != c.notes {
			t.Errorf("%s: notes = %v, want %d", c.name, r.Notes, c.notes)
		}
	}
}
