package scoring

import (
	"reflect"
	"testing"
)

func TestMatchSkills(t *testing.T) {
	profile := NewSkillSet("Python, PyTorch, C++ and Kubernetes. Built distributed training on AWS. Machine learning research.")
	matched, missing := MatchSkills(profile, []string{
		"Python", "C++", "Distributed systems", "Machine Learning", "Go", "Kubernetes (K8s)", "Software engineering", "", "Rust",
	})
	if want := []string{"Python", "C++", "Distributed systems", "Machine Learning", "Kubernetes (K8s)"}; !reflect.DeepEqual(matched, want) {
		t.Errorf("matched = %v, want %v", matched, want)
	}
	if want := []string{"Go", "Software engineering", "Rust"}; !reflect.DeepEqual(missing, want) {
		t.Errorf("missing = %v, want %v", missing, want)
	}
	if m, x := MatchSkills(profile, nil); len(m) != 0 || len(x) != 0 || m == nil || x == nil {
		t.Errorf("no skills should give two empty lists, got %v %v", m, x)
	}
}

func TestTailoredFit(t *testing.T) {
	raw := `{"ratings": {"role": 9, "seniority": 8, "skills": 6, "domain": 7, "location": 10, "blocker": false}, "reason": "close"}`
	required := []string{"Go", "Kubernetes", "PostgreSQL", "Terraform", "Kafka"}
	before, after, ok := TailoredFit(raw, "Backend engineer: Go and PostgreSQL.", "Ran Kafka pipelines on Kubernetes.", required)
	if !ok {
		t.Fatal("ratings not read")
	}
	if after <= before {
		t.Errorf("more of the job's skills shown: after %d should beat before %d", after, before)
	}
	// Nothing new shown: the same score.
	if b, a, _ := TailoredFit(raw, "Go and PostgreSQL.", "Go.", required); a != b {
		t.Errorf("no new skills: before %d, after %d", b, a)
	}
	if _, _, ok := TailoredFit(`{"fit_score": 80}`, "", "", required); ok {
		t.Error("a reply without ratings gave a score")
	}
}
