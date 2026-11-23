// Command dispatch for `wera discover`: probes all three ATS job board
// APIs with candidate slugs derived from a company name.
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"wera/internal/config"
	"wera/internal/pipeline"
	"wera/internal/sources"
)

// runDiscover implements `wera discover <name> [slug]`: it derives
// candidate board slugs from the company name and probes Greenhouse, Lever
// and Ashby, printing every endpoint that returns jobs (PLAN.md section 11).
func runDiscover(ctx context.Context, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: wera discover <name> [slug]")
	}
	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	registry := pipeline.NewSourceRegistry(env)
	slugs := candidateSlugs(args[0], args[1:]...)
	if len(slugs) == 0 {
		return fmt.Errorf("no candidate slugs could be derived from %q", args[0])
	}

	fmt.Printf("probing %q (candidate slugs: %s)\n", args[0], strings.Join(slugs, ", "))
	anyFound := false
	for _, ats := range []string{"greenhouse", "lever", "ashby"} {
		src := registry[ats]
		found := false
		for _, slug := range slugs {
			var jobs []sources.RawJob
			var err error
			if ds, ok := src.(sources.DetailSource); ok {
				jobs, err = ds.List(ctx, slug) // listing is enough to confirm a board
				if errors.Is(err, sources.ErrPartialList) && len(jobs) > 0 {
					err = nil
				}
			} else {
				jobs, err = src.Fetch(ctx, slug)
			}
			if err != nil {
				if errors.Is(err, sources.ErrBoardNotFound) {
					continue // expected for wrong slugs
				}
				fmt.Printf("  %-10s %s: error: %v\n", ats, slug, err)
				continue
			}
			fmt.Printf("  %-10s %s: FOUND %d jobs", ats, slug, len(jobs))
			if len(jobs) > 0 {
				fmt.Printf(" (e.g. %q)", jobs[0].Title)
			}
			fmt.Println()
			found = true
			anyFound = true
			break // first hit per ATS is enough
		}
		if !found {
			fmt.Printf("  %-10s no board found for any candidate slug\n", ats)
		}
	}

	if anyFound {
		fmt.Println("suggestion: add the matching token to config/companies.yaml (enabled: false until reviewed).")
	} else {
		fmt.Println("no boards found on any ATS; this company may use a custom career site.")
	}
	return nil
}

// corporateSuffixes are trailing words dropped when deriving extra slug
// candidates ("Fireworks AI" -> also try "fireworks").
var corporateSuffixes = map[string]bool{
	"ai": true, "labs": true, "lab": true, "inc": true, "co": true,
	"corp": true, "llc": true, "technologies": true, "systems": true,
	"trading": true, "capital": true, "hq": true, "group": true,
}

// candidateSlugs derives board-slug candidates from a company name:
// concatenated ("fireworksai"), hyphenated ("fireworks-ai"), and the same
// with common corporate suffixes dropped ("fireworks"). An explicitly
// provided slug is tried first.
func candidateSlugs(name string, provided ...string) []string {
	var slugs []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, existing := range slugs {
			if existing == s {
				return
			}
		}
		slugs = append(slugs, s)
	}
	for _, p := range provided {
		if p != "" {
			add(strings.ToLower(strings.TrimSpace(p)))
		}
	}

	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	add(strings.Join(words, ""))
	add(strings.Join(words, "-"))
	trimmed := slices.Clone(words)
	for len(trimmed) > 1 && corporateSuffixes[trimmed[len(trimmed)-1]] {
		trimmed = trimmed[:len(trimmed)-1]
		add(strings.Join(trimmed, ""))
		add(strings.Join(trimmed, "-"))
	}
	return slugs
}
