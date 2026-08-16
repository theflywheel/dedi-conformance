package suite

import (
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/dedi-conformance/internal/manifest"
	"github.com/theflywheel/dedi-conformance/internal/report"
	"github.com/theflywheel/dedi-conformance/internal/spec"
	"github.com/theflywheel/dedi-conformance/mock"
)

const specPath = "../../spec/api/openapi.yaml"

// serverProfiles are the profiles the mock node can answer for. Publication is
// excluded: it verifies a real publisher's well-known over TLS, which is not
// something a local mock stands in for honestly.
var serverProfiles = []string{manifest.ProfileCore, manifest.ProfileVersioning, manifest.ProfileBeckn}

func runAgainst(t *testing.T, defects ...mock.Defect) *report.Run {
	t.Helper()
	srv := mock.Server(defects...)
	t.Cleanup(srv.Close)

	m := &manifest.Manifest{
		BaseURL:  srv.URL,
		Profiles: serverProfiles,
		Fixtures: map[string]string{
			manifest.Namespace:          mock.Namespace,
			manifest.Registry:           mock.Registry,
			manifest.Record:             mock.Record,
			manifest.RecordWithVersions: mock.RecordWithVersions,
			manifest.RevokedRecord:      mock.RevokedRecord,
			manifest.AbsentNamespace:    mock.AbsentNamespace,
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("the test's own manifest is invalid: %v", err)
	}
	s, err := spec.LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	return New(m, s, 10*time.Second).Execute()
}

// TestAConformantNodePasses is the floor. If this fails, every other test here
// is measuring the mock rather than the suite.
func TestAConformantNodePasses(t *testing.T) {
	run := runAgainst(t)
	if !run.Passed() {
		for _, r := range run.Results {
			if r.Outcome == report.Fail {
				t.Errorf("conformant node failed %q:\n  %s\n  %s", r.Case, r.Request, r.Detail)
			}
		}
	}
	// A run that executed nothing must never read as a pass.
	if len(run.Results) == 0 {
		t.Fatal("the run produced no results at all")
	}
}

// TestEveryCaseActuallyRuns guards against a case that skips on every
// deployment: a skip is not a pass, but a suite made entirely of skips still
// reports no failures, and that is the shape a vacuous suite takes.
func TestEveryCaseActuallyRuns(t *testing.T) {
	run := runAgainst(t)
	for _, r := range run.Results {
		if r.Outcome == report.Skip {
			t.Errorf("case %q skipped against a fully conformant node: %s\n"+
				"a case that cannot run even here will never catch anything", r.Case, r.Detail)
		}
	}
}

// TestEachDefectIsCaught is the anti-vacuity test, and the reason the mock
// exists. Each defect is a real way an implementation can be wrong; the named
// case must fail on it. Weakening an assertion turns one of these red.
func TestEachDefectIsCaught(t *testing.T) {
	cases := []struct {
		defect mock.Defect
		// wantCase is a substring of the case name that must fail.
		wantCase string
		why      string
	}{
		{mock.AbsentIs200, "absent namespace is 404",
			"a relying party cannot tell an unregistered identity from an empty one"},
		{mock.NoEnvelope, "message/data envelope",
			"a client written against the spec cannot find the payload"},
		{mock.MissingRecordFields, "fields a client resolves on",
			"the answer cannot be acted on"},
		{mock.RejectsSpecEnum, "documented enum values are accepted",
			"a spec-conformant client is rejected"},
		{mock.VersionIDIs400, "version_id the contract permits",
			"a well-formed request is reported as malformed"},
		{mock.NoErrorCode, "machine-readable code",
			"failures can only be told apart by parsing prose"},
		{mock.BadState, "fields a client resolves on",
			"an undocumented state cannot be interpreted"},
		{mock.RevokedIs404, "revoked record is still resolvable",
			"a revocation is indistinguishable from never having existed"},
		{mock.AsOnIgnored, "as_on returns the version",
			"the directory cannot be asked what it said in the past"},
	}

	for _, tc := range cases {
		t.Run(string(tc.defect), func(t *testing.T) {
			run := runAgainst(t, tc.defect)
			if run.Passed() {
				t.Fatalf("the suite passed a node with the %q defect — %s", tc.defect, tc.why)
			}
			var failed []string
			for _, r := range run.Results {
				if r.Outcome == report.Fail {
					failed = append(failed, r.Case)
				}
			}
			found := false
			for _, name := range failed {
				if strings.Contains(name, tc.wantCase) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("the %q defect was caught, but not by the case meant to catch it\n"+
					"want a failure in a case matching %q; failing cases were: %v\n"+
					"a defect caught only as collateral damage means the named case is not doing its job",
					tc.defect, tc.wantCase, failed)
			}
		})
	}
}

// TestAFailureCarriesItsEvidence: a conformance result is a claim other people
// act on, so a failure that cannot be reproduced by hand is not usable.
func TestAFailureCarriesItsEvidence(t *testing.T) {
	run := runAgainst(t, mock.AbsentIs200)
	for _, r := range run.Results {
		if r.Outcome != report.Fail {
			continue
		}
		if r.Request == "" {
			t.Errorf("case %q failed without recording the request that produced it", r.Case)
		}
		if strings.TrimSpace(r.Detail) == "" {
			t.Errorf("case %q failed with no explanation", r.Case)
		}
	}
}

// TestAProfileWithNoCasesIsNotAPass: the most dangerous possible bug in a
// conformance tool is reporting success for work it never did.
func TestAProfileWithNoCasesIsNotAPass(t *testing.T) {
	run := &report.Run{}
	if run.ProfilePassed(manifest.ProfileCore) {
		t.Error("a profile that ran no cases reported as passed")
	}
	if run.Passed() {
		t.Error("an empty run reported as passed")
	}
}
