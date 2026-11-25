package profile

import "testing"

func TestAddSkill(t *testing.T) {
	const base = "# Candidate Profile\n## Target roles\nSRE.\n\n## Technical skills\n- Languages: Go, Python\n- Cloud: AWS\n\n## Experience\n- Built C++ tools\n"
	got, changed := AddSkill(base, "Terraform")
	want := "# Candidate Profile\n## Target roles\nSRE.\n\n## Technical skills\n- Languages: Go, Python\n- Cloud: AWS\n- Also: Terraform\n\n## Experience\n- Built C++ tools\n"
	if !changed || got != want {
		t.Fatalf("first addition:\n%q\nwant\n%q", got, want)
	}
	got, changed = AddSkill(got, "Kubernetes")
	if !changed || got != "# Candidate Profile\n## Target roles\nSRE.\n\n## Technical skills\n- Languages: Go, Python\n- Cloud: AWS\n- Also: Terraform, Kubernetes\n\n## Experience\n- Built C++ tools\n" {
		t.Fatalf("second addition: %q", got)
	}
	for _, dup := range []string{"go", "PYTHON", "terraform", "Kubernetes"} {
		if _, changed := AddSkill(got, dup); changed {
			t.Errorf("%q was added again", dup)
		}
	}
	// Whole words only: "Go" in "Google" or "C" in "C++" doesn't count,
	// and a mention outside the Skills section doesn't either.
	if _, changed := AddSkill("## Skills\n- Google Cloud\n", "Go"); !changed {
		t.Error("Go matched inside Google")
	}
	if _, changed := AddSkill(base, "C"); !changed {
		t.Error("C matched C++ outside the section")
	}
	if _, changed := AddSkill("## Skills\n- C++, C#\n", "C"); !changed {
		t.Error("C matched C++ or C#")
	}

	// No Skills section: one goes before Experience, or at the end.
	got, _ = AddSkill("# P\n## Target roles\nX.\n\n## Experience\n- Y\n", "SQL")
	if got != "# P\n## Target roles\nX.\n\n## Skills\n- Also: SQL\n\n## Experience\n- Y\n" {
		t.Errorf("before Experience: %q", got)
	}
	got, _ = AddSkill("# P\n## Target roles\nX.", "SQL")
	if got != "# P\n## Target roles\nX.\n\n## Skills\n- Also: SQL" {
		t.Errorf("at the end: %q", got)
	}
}

func TestCleanSkill(t *testing.T) {
	for _, c := range []struct{ in, want string }{{"  Apache   Kafka ", "Apache Kafka"}, {"C++", "C++"}, {"Node.js", "Node.js"}} {
		if got, err := CleanSkill(c.in); err != nil || got != c.want {
			t.Errorf("CleanSkill(%q) = %q, %v", c.in, got, err)
		}
	}
	for _, bad := range []string{"", "   ", "## Experience", "a\nb", "<script>", "---", "this skill name is far too long to be a single skill really, honestly"} {
		if got, err := CleanSkill(bad); err == nil && bad != "a\nb" {
			t.Errorf("CleanSkill(%q) = %q, want an error", bad, got)
		}
	}
	// A line break inside is spacing, not a second line.
	if got, _ := CleanSkill("a\nb"); got != "a b" {
		t.Errorf("line break: %q", got)
	}
}
