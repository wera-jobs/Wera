// Package pay reads the pay a job posting states from its text: the same
// rules as the Chrome extension (wera-extension src/content/facts.ts), so
// the dashboard and the extension agree on a posting's pay. Only a figure
// with a pay word near it, and nothing that makes it funding, revenue or a
// benefit, counts; and only amounts that are a plausible wage.
package pay

import (
	"regexp"
	"strconv"
	"strings"
)

// Pay is a posting's stated pay: one figure (Min == Max) or a range.
type Pay struct {
	Min, Max float64
	Currency string // ISO code: USD, CAD, AUD, NZD, SGD, HKD
	Period   string // hour or year
}

const currency = `USD|CAD|AUD|NZD|SGD|HKD`

var (
	// A figure ("$120,000", "$120k", "45.50"), its currency before or after it ("CAD $88,000", "$70,000 CAD"),
	// an optional top ("- $150,000", "to 150K": a range's top may leave out its sign), and the period.
	rangeRe = regexp.MustCompile(`(?:(?P<cur0>` + currency + `)\s?)?\$\s?(?P<lo>\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?)\s?(?P<lok>[kK])?` +
		`(?:\s*(?P<cur1>` + currency + `)\b)?` +
		`(?:\s*(?:-|–|—|to|and)\s*(?:(?:` + currency + `)\s?)?\$?\s?(?P<hi>\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?)\s?(?P<hik>[kK])?)?` +
		`(?:\s*(?P<cur2>` + currency + `)\b)?\s*(?:(?:per|an|a|/)\s*(?P<per>hour|hr|year|yr|annum|month|mo)\b)?`)
	// Words that say a figure is yearly, near it: an hourly reading of it is wrong (a typo such as "$104,00").
	yearWords = regexp.MustCompile(`(?i)\b(annual(?:ly)?|yearly|per year|a year)\b`)
	// Words that make a dollar figure a salary, near it.
	payWords = regexp.MustCompile(`(?i)\b(salary|salaries|pay|compensation|base|wage|wages|hourly|annual(?:ly)?|per (?:hour|year|annum)|ote|earn|range|salaire|rémunération|salario|gehalt)\b`)
	// Words that make it something else: funding, revenue, a benefit, a bonus.
	notPay   = regexp.MustCompile(`(?i)\b(raised|funding|funded|series [a-e]|revenue|valuation|billion|million|budget|stipend|reimburse\w*|allowance|bonus|401\s?\(?k|match|per (?:employee|month for)|grant|credit|deductible|donat\w*|market cap|assets)\b`)
	stopRe   = regexp.MustCompile(`[.!?]\s`)
	stopEnd  = regexp.MustCompile(`[.!?](\s|$)`)
	stopChar = regexp.MustCompile(`[.!?]`)
)

// sentence is the sentence around text[from:to], at most 120 bytes before
// it and 60 after. Lines do not end it: postings put "Pay range:" on a
// line of its own above the figures.
func sentence(text string, from, to int) string {
	before := text[max(0, from-120):from]
	// Start after the last ". " whose remainder has no sentence end of its own.
	cut := 0
	for _, m := range stopRe.FindAllStringIndex(before, -1) {
		if !stopChar.MatchString(before[m[1]:]) {
			cut = m[1]
		}
	}
	after := text[to:min(len(text), to+60)]
	if loc := stopEnd.FindStringIndex(after); loc != nil {
		after = after[:loc[0]]
	}
	return before[cut:] + text[from:to] + after
}

func num(digits, k string) float64 {
	n, _ := strconv.ParseFloat(strings.ReplaceAll(digits, ",", ""), 64)
	if k != "" {
		n *= 1000
	}
	return n
}

// FromText returns the first pay range the text states, and whether there is one.
func FromText(text string) (Pay, bool) {
	// A sentence whose figures don't add up (a typo) is skipped whole, not read from its next figure.
	skipBefore := 0
	for _, m := range rangeRe.FindAllStringSubmatchIndex(text, -1) {
		if m[0] < skipBefore {
			continue
		}
		g := func(name string) string {
			i := rangeRe.SubexpIndex(name)
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		near := sentence(text, m[0], m[1])
		if !payWords.MatchString(near) || notPay.MatchString(near) {
			continue
		}
		loDigits, loK, hiDigits, hiK, per := g("lo"), g("lok"), g("hi"), g("hik"), strings.ToLower(g("per"))
		cur1 := g("cur0")
		if cur1 == "" {
			cur1 = g("cur1")
		}
		cur2 := g("cur2")
		lo := num(loDigits, loK)
		hi := 0.0
		if hiDigits != "" {
			k := hiK
			if k == "" {
				k = loK
			}
			hi = num(hiDigits, k)
		}
		if loK == "" && hiK != "" && lo < 1000 { // "$120-150K"
			lo *= 1000
		}
		unit := per
		if unit == "" {
			switch {
			case lo >= 15_000:
				unit = "year"
			case lo >= 7 && lo <= 300:
				unit = "hour"
			}
		}
		// An hour inferred from a small figure is wrong where the sentence says the pay is yearly.
		if per == "" && unit == "hour" && yearWords.MatchString(near) {
			skipBefore = m[1] + 60
			continue
		}
		hourly := unit == "hour" || unit == "hr"
		yearly := unit == "year" || unit == "yr" || unit == "annum"
		switch {
		case hourly && (lo < 7 || lo > 500), yearly && (lo < 15_000 || lo > 2_000_000), !hourly && !yearly:
			continue
		}
		if hi != 0 && (hi < lo || hi > lo*4) {
			continue
		}
		cur := cur2
		if cur == "" {
			cur = cur1
		}
		if cur == "" {
			cur = "USD"
		}
		p := Pay{Min: lo, Max: lo, Currency: strings.ToUpper(cur), Period: "year"}
		if hi != 0 {
			p.Max = hi
		}
		if hourly {
			p.Period = "hour"
		}
		return p, true
	}
	return Pay{}, false
}
