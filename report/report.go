// Package report models the outcome of a conformance run and renders it.
//
// A conformance result is a claim other people rely on, so the model keeps the
// evidence next to the verdict: every failure carries the request that produced
// it and what was expected, and every skip carries why it was skipped. A bare
// pass/fail count is not something a reader can check.
package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Outcome is what happened to one case.
type Outcome string

const (
	Pass Outcome = "pass"
	Fail Outcome = "fail"
	// Skip is for a case that could not run — a fixture the deployment does
	// not have, a capability out of profile. Kept distinct from Pass because
	// reporting "nothing went wrong" for a case that never executed is how a
	// suite ends up vacuously green.
	Skip Outcome = "skip"
)

// Result is one case's outcome.
type Result struct {
	Profile string  `json:"profile"`
	Case    string  `json:"case"`
	Outcome Outcome `json:"outcome"`
	// Detail explains a failure or a skip. Empty on a pass.
	Detail string `json:"detail,omitempty"`
	// Request is the HTTP request the case made, when there was one, so a
	// failure can be reproduced by hand.
	Request string `json:"request,omitempty"`
	// SpecRef points at what the case is enforcing.
	SpecRef string `json:"spec_ref,omitempty"`
}

// Run is a whole conformance run.
type Run struct {
	BaseURL  string   `json:"base_url"`
	SpecSHA  string   `json:"spec_sha,omitempty"`
	Profiles []string `json:"profiles"`
	Results  []Result `json:"results"`
}

// Add records one case outcome.
func (r *Run) Add(res Result) { r.Results = append(r.Results, res) }

// ProfilePassed reports whether every case in a profile passed. A profile with
// no cases at all is not a pass — it means the suite ran nothing, which must
// never read as conformance.
func (r *Run) ProfilePassed(profile string) bool {
	ran := false
	for _, res := range r.Results {
		if res.Profile != profile {
			continue
		}
		ran = true
		if res.Outcome == Fail {
			return false
		}
	}
	return ran
}

// Counts returns pass, fail and skip totals for a profile.
func (r *Run) Counts(profile string) (pass, fail, skip int) {
	for _, res := range r.Results {
		if res.Profile != profile {
			continue
		}
		switch res.Outcome {
		case Pass:
			pass++
		case Fail:
			fail++
		case Skip:
			skip++
		}
	}
	return
}

// Passed reports whether the whole run passed.
func (r *Run) Passed() bool {
	for _, p := range r.claimedProfiles() {
		if !r.ProfilePassed(p) {
			return false
		}
	}
	return len(r.Results) > 0
}

func (r *Run) claimedProfiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, res := range r.Results {
		if !seen[res.Profile] {
			seen[res.Profile] = true
			out = append(out, res.Profile)
		}
	}
	return out
}

// WriteHuman renders the report a person reads.
func (r *Run) WriteHuman(w io.Writer) {
	fmt.Fprintf(w, "DeDi conformance — %s\n", r.BaseURL)
	if r.SpecSHA != "" {
		fmt.Fprintf(w, "spec: %s\n", r.SpecSHA)
	}
	fmt.Fprintln(w)

	for _, p := range r.claimedProfiles() {
		pass, fail, skip := r.Counts(p)
		mark := "PASS"
		if !r.ProfilePassed(p) {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "%-6s %-14s %d passed", mark, p, pass)
		if fail > 0 {
			fmt.Fprintf(w, ", %d failed", fail)
		}
		if skip > 0 {
			fmt.Fprintf(w, ", %d skipped", skip)
		}
		fmt.Fprintln(w)
	}

	// Failures in full, with the request that produced each, because a report
	// that only counts them cannot be acted on.
	var failures, skips []Result
	for _, res := range r.Results {
		switch res.Outcome {
		case Fail:
			failures = append(failures, res)
		case Skip:
			skips = append(skips, res)
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(w, "\nFailures\n")
		for _, f := range failures {
			fmt.Fprintf(w, "\n  %s / %s\n", f.Profile, f.Case)
			if f.Request != "" {
				fmt.Fprintf(w, "    request:  %s\n", f.Request)
			}
			for _, line := range strings.Split(strings.TrimRight(f.Detail, "\n"), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if f.SpecRef != "" {
				fmt.Fprintf(w, "    spec:     %s\n", f.SpecRef)
			}
		}
	}
	if len(skips) > 0 {
		fmt.Fprintf(w, "\nSkipped\n")
		for _, s := range skips {
			fmt.Fprintf(w, "  %s / %s — %s\n", s.Profile, s.Case, s.Detail)
		}
	}

	fmt.Fprintln(w)
	if r.Passed() {
		fmt.Fprintf(w, "Result: conformant — %s\n", strings.Join(r.claimedProfiles(), ", "))
	} else {
		fmt.Fprintf(w, "Result: NOT conformant\n")
	}
}

// WriteJSON renders the machine-readable report.
func (r *Run) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// JUnit rendering, so a run drops into any CI that understands test reports.

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name  string      `xml:"name,attr"`
	Tests int         `xml:"tests,attr"`
	Fails int         `xml:"failures,attr"`
	Skips int         `xml:"skipped,attr"`
	Cases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// WriteJUnit renders the report as a JUnit XML document.
func (r *Run) WriteJUnit(w io.Writer) error {
	byProfile := map[string][]Result{}
	for _, res := range r.Results {
		byProfile[res.Profile] = append(byProfile[res.Profile], res)
	}
	profiles := make([]string, 0, len(byProfile))
	for p := range byProfile {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)

	doc := junitSuites{}
	for _, p := range profiles {
		results := byProfile[p]
		pass, fail, skip := r.Counts(p)
		s := junitSuite{Name: "dedi-conformance." + p, Tests: pass + fail + skip, Fails: fail, Skips: skip}
		for _, res := range results {
			c := junitCase{Name: res.Case, Classname: "dedi-conformance." + p}
			body := res.Detail
			if res.Request != "" {
				body = res.Request + "\n" + body
			}
			switch res.Outcome {
			case Fail:
				c.Failure = &junitMessage{Message: firstLine(res.Detail), Body: body}
			case Skip:
				c.Skipped = &junitMessage{Message: firstLine(res.Detail), Body: body}
			}
			s.Cases = append(s.Cases, c)
		}
		doc.Suites = append(doc.Suites, s)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
