// Package interview holds interview practice that ships with Wera rather
// than living in the database: the role tracks a user practices for, and
// the workplace email simulations (scenarios.json).
//
// Why tracks are roles, not stacks: an interview loop is shaped by the
// role (a backend loop has system design and databases, a frontend one has
// the browser and accessibility, a PM one has product sense), while the
// stack mostly changes the language of the coding round, which a
// multiple-choice format cannot test well. So a track picks the domains
// and the simulations; the stack is a finer filter on top, as tags on
// questions (go, react, kubernetes, ...) that a round prefers.
package interview

import "slices"

// Track is one kind of interview loop.
type Track struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Blurb string `json:"blurb"`
	// Domains in the order the loop weighs them.
	Domains []string `json:"domains"`
	// Role families (config/roles.yaml) this track suits, for the suggestion.
	families []string
}

// Domains of the question bank (interview_questions.domain).
const (
	DSA        = "Data structures & algorithms"
	Design     = "System design"
	Databases  = "Databases"
	OSNet      = "Operating systems & networking"
	ML         = "Machine learning"
	Behavioral = "Behavioral"
	Web        = "Web & frontend"
	Backend    = "APIs & backend"
	Cloud      = "Cloud & DevOps"
	Mobile     = "Mobile"
	Security   = "Security"
	Data       = "Data & analytics"
	Product    = "Product sense"
)

// Tracks are the role tracks, most asked-for first. General is the fallback for every role
// Wera matches that is not in tech: its practice is the workplace itself.
var Tracks = []Track{
	{ID: "backend", Label: "Backend engineer", Blurb: "APIs, databases and system design, plus the on-call and code review moments.",
		Domains:  []string{Backend, Databases, Design, DSA, OSNet, Security, Behavioral},
		families: []string{"software_engineering", "swe_infra", "embedded", "qa_test", "solutions", "early_career"}},
	{ID: "frontend", Label: "Frontend engineer", Blurb: "JavaScript, React, the browser and accessibility, and working with design.",
		Domains: []string{Web, DSA, Security, Design, Behavioral}},
	{ID: "fullstack", Label: "Full-stack engineer", Blurb: "Both sides of the API, and the contracts between them.",
		Domains: []string{Web, Backend, Databases, Design, DSA, Behavioral}},
	{ID: "data", Label: "Data & ML", Blurb: "SQL, statistics, experiments and models, and telling people what the numbers mean.",
		Domains:  []string{Data, ML, Databases, DSA, Behavioral},
		families: []string{"ml_infra", "ml_engineering", "data_science", "data_engineering", "data_analytics", "research", "quant"}},
	{ID: "infra", Label: "DevOps & SRE", Blurb: "Containers, the cloud, Linux and incidents: keeping it up and keeping it cheap.",
		Domains:  []string{Cloud, OSNet, Design, Security, Databases, Behavioral},
		families: []string{"sre", "platform", "devops", "infrastructure", "production", "trading_ops", "it_networking", "security"}},
	{ID: "mobile", Label: "Mobile engineer", Blurb: "iOS and Android, offline data and releases you cannot take back.",
		Domains: []string{Mobile, Backend, DSA, Security, Behavioral}},
	{ID: "product", Label: "Product & program", Blurb: "Product sense, metrics and prioritizing between people who all need it first.",
		Domains:  []string{Product, Data, Design, Behavioral},
		families: []string{"product_management", "program_management", "design"}},
	{ID: "general", Label: "Any role", Blurb: "Behavioral questions and the everyday emails every new job brings.",
		Domains: []string{Behavioral}},
}

// TrackByID finds a track; ok is false for an unknown id.
func TrackByID(id string) (Track, bool) {
	i := slices.IndexFunc(Tracks, func(t Track) bool { return t.ID == id })
	if i < 0 {
		return Track{}, false
	}
	return Tracks[i], true
}

// Suggest picks the track for a profile's role families: the first family
// a track names wins, and anything outside tech practices the general one.
func Suggest(families []string) string {
	for _, f := range families {
		for _, t := range Tracks {
			if slices.Contains(t.families, f) {
				return t.ID
			}
		}
	}
	return "general"
}
