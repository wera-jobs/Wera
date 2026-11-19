package pay

import "testing"

// The same cases as the extension's payFromText (wera-extension src/content/facts.test.ts).
func TestFromText(t *testing.T) {
	cases := []struct {
		text     string
		min, max float64
		cur, per string
	}{
		{"The base salary range for this role is $120,000 - $150,000.", 120000, 150000, "USD", "year"},
		{"Compensation: $165K–$210K + equity", 165000, 210000, "USD", "year"},
		{"Pay range: $25.50 to $31 per hour", 25.5, 31, "USD", "hour"},
		{"The hourly pay for this position is $22/hr.", 22, 22, "USD", "hour"},
		{"Salary Range: $95,000 to $112,500 USD per year", 95000, 112500, "USD", "year"},
		{"Annual salary: $80,000", 80000, 80000, "USD", "year"},
		{"Salary: $120-150K DOE", 120000, 150000, "USD", "year"},
		{"The proposed salary range for this position is:\n\n$86,600 - $181,800\n\nCACI is an Equal Opportunity Employer.", 86600, 181800, "USD", "year"},
		{"Salary Range $220,000.00 - $275,000.00 A successful applicant", 220000, 275000, "USD", "year"},
		{"Base pay rates will be between $110,000 and $190,000 per year.", 110000, 190000, "USD", "year"},
		{"Salary range is expected to be between $125,100.00 CAD - $175,100.00 CAD", 125100, 175100, "CAD", "year"},
		{"Salary range: $70,000 - $80,000 CAD per year.", 70000, 80000, "CAD", "year"},
		{"We raised $40M last year. The salary for this role is $140,000 - $160,000.", 140000, 160000, "USD", "year"},
		// Found on real postings: the currency before the sign, and a range written with it twice.
		{"La gamme de salaire est de CAD $88,000 - CAD $110,000. Les échelles", 88000, 110000, "CAD", "year"},
		{"Base salary: USD $150,000 to $180,000 per year.", 150000, 180000, "USD", "year"},
	}
	for _, c := range cases {
		got, ok := FromText(c.text)
		if !ok || got.Min != c.min || got.Max != c.max || got.Currency != c.cur || got.Period != c.per {
			t.Errorf("%q: got %+v %v, want %v-%v %s %s", c.text, got, ok, c.min, c.max, c.cur, c.per)
		}
	}
	for _, text := range []string{
		"We raised $40 million in our Series B from great investors.",
		"Our customers process $2 billion in payments every year.",
		"We offer a $1,500 learning stipend and a 401(k) match.",
		"Includes a $500 home office allowance on top of your salary.",
		"Join a team of 50 engineers building payments.",
		"Annual revenue of $300,000,000 and growing.",
		"Pay your rent with $5 off.",
		"AECOM is a Fortune 500 firm that had revenue of $16.1 billion in fiscal year 2025.",
		"Project budgets will vary from $50K to $30M+.",
		// A typo in a yearly range ("$104,00") is no hourly wage.
		"The annual base salary range for this role based in Chicago is $104,00-$113,000.",
	} {
		if got, ok := FromText(text); ok {
			t.Errorf("%q: found pay %+v", text, got)
		}
	}
}
