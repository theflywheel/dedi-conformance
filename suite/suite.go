// Package suite runs conformance cases against a live DeDi implementation.
//
// Everything here talks to the implementation under test over HTTP against a
// base URL. Nothing is imported from any particular implementation, and the
// suite never writes: it reads the endpoints the standard defines, at names the
// manifest supplies. That is what makes the same binary usable against our node
// and against someone else's.
package suite

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/theflywheel/dedi-conformance/manifest"
	"github.com/theflywheel/dedi-conformance/openapi"
	"github.com/theflywheel/dedi-conformance/report"
)

// Suite holds everything a run needs.
type Suite struct {
	M    *manifest.Manifest
	Spec *openapi.Spec
	// SpecPath is where Spec was loaded from. The reference registry schemas
	// sit next to it (../schemas), and cases that check a registry's shape
	// read them rather than transcribing their contents.
	SpecPath string
	Client   *http.Client
	Run      *report.Run
}

// New builds a Suite. timeout bounds each individual request: a node that
// hangs should fail its case, not the whole run.
func New(m *manifest.Manifest, s *openapi.Spec, specPath string, timeout time.Duration) *Suite {
	return &Suite{
		M:        m,
		Spec:     s,
		SpecPath: specPath,
		Client:   &http.Client{Timeout: timeout},
		Run:      &report.Run{BaseURL: m.BaseURL, Profiles: m.Profiles},
	}
}

// caseFn is one conformance case.
type caseFn func(t *T)

// T is the per-case context. It deliberately mirrors testing.T's shape —
// Errorf, Fatalf, Skipf — because that is the vocabulary anyone writing a case
// already has, but it records into a report rather than a test binary.
type T struct {
	s       *Suite
	profile string
	name    string
	specRef string

	failed  bool
	skipped bool
	detail  strings.Builder
	request string

	// abort unwinds to the case runner on Fatalf/Skipf.
	abort bool
}

type abortErr struct{}

// Errorf records a failure and lets the case continue, so one run reports
// every problem it can see rather than only the first.
func (t *T) Errorf(format string, args ...any) {
	t.failed = true
	fmt.Fprintf(&t.detail, format+"\n", args...)
}

// Fatalf records a failure and stops the case — for when continuing would only
// produce noise derived from the same fault.
func (t *T) Fatalf(format string, args ...any) {
	t.Errorf(format, args...)
	t.abort = true
	panic(abortErr{})
}

// Skipf records that the case could not run, and why. A skip is never a pass.
func (t *T) Skipf(format string, args ...any) {
	t.skipped = true
	fmt.Fprintf(&t.detail, format, args...)
	t.abort = true
	panic(abortErr{})
}

// SpecRef attaches what this case is enforcing, shown on failure.
func (t *T) SpecRef(ref string) { t.specRef = ref }

// Fixture resolves a fixture role for this deployment.
func (t *T) Fixture(role string) string { return t.s.M.Fixture(role) }

// runCase executes one case and records its result.
func (s *Suite) runCase(profile, name string, fn caseFn) {
	t := &T{s: s, profile: profile, name: name}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(abortErr); !ok {
					panic(r) // a real bug in the suite; do not swallow it
				}
			}
		}()
		fn(t)
	}()

	res := report.Result{Profile: profile, Case: name, Request: t.request, SpecRef: t.specRef,
		Detail: strings.TrimRight(t.detail.String(), "\n")}
	switch {
	case t.failed:
		res.Outcome = report.Fail
	case t.skipped:
		res.Outcome = report.Skip
	default:
		res.Outcome = report.Pass
	}
	s.Run.Add(res)
}

// Response is one HTTP exchange, decoded as far as it could be.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	// JSON is the decoded body, nil if it did not parse as a JSON object.
	JSON map[string]any
}

// Envelope returns the response's "data" field.
func (r *Response) Envelope() (any, bool) {
	if r.JSON == nil {
		return nil, false
	}
	v, ok := r.JSON["data"]
	return v, ok
}

// GET performs a request against the implementation under test and records it
// on the case, so a failure report carries the exact call that produced it.
func (t *T) GET(path string, query url.Values) *Response {
	u := strings.TrimRight(t.s.M.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	t.request = "GET " + u

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("could not build a request for %s: %v", u, err)
	}
	for k, v := range t.s.M.Headers {
		req.Header.Set(k, v)
	}
	resp, err := t.s.Client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: reading body: %v", u, err)
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		out.JSON = obj
	}
	return out
}

// wantStatus asserts the status and stops the case if it differs — subsequent
// assertions about the body of a response that should not exist are noise.
func (t *T) wantStatus(r *Response, want int) {
	if r.Status != want {
		t.Fatalf("status %d, want %d\nbody: %s", r.Status, want, truncate(r.Body, 400))
	}
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Execute runs every case for every claimed profile and returns the report.
func (s *Suite) Execute() *report.Run {
	for _, profile := range manifest.KnownProfiles {
		if !s.M.Claims(profile) {
			continue
		}
		for _, c := range casesFor(profile) {
			s.runCase(profile, c.name, c.fn)
		}
	}
	return s.Run
}

type namedCase struct {
	name string
	fn   caseFn
}

func casesFor(profile string) []namedCase {
	switch profile {
	case manifest.ProfileCore:
		return coreCases
	case manifest.ProfileVersioning:
		return versioningCases
	case manifest.ProfilePublication:
		return publicationCases
	case manifest.ProfileBeckn:
		return becknCases
	}
	return nil
}
