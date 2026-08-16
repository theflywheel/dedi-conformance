// Package manifest describes the implementation under test: where it is, what
// it claims to support, and where in its data the suite can find each thing it
// needs to look at.
//
// The manifest exists because of one constraint that shapes this whole tool:
// DeDi standardises a *read* API and says nothing about how records get in.
// Every implementation has its own write path — ours is a signed append log,
// someone else's is a static file build step, dedi.global's is a SaaS console.
// A conformance suite that seeded its own fixtures would therefore be testing
// a write API that is not in the standard, and would exclude every conformant
// implementation that does not happen to share ours.
//
// So the suite does not create data. The operator points it at data that
// already exists, by mapping the abstract roles the cases need
// ("a record with more than one version") onto concrete names in their
// deployment. That is the only coupling, it is declarative, and it works
// against a public node with no credentials at all.
package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Manifest is the on-disk description an implementer writes.
type Manifest struct {
	// BaseURL is the root of the implementation under test, e.g.
	// "https://dedi.example.org". Paths from the spec are appended to it.
	BaseURL string `json:"base_url"`

	// Profiles are the conformance profiles this implementation claims.
	// Claiming a profile is what opts its cases in: the suite never fails an
	// implementation for lacking a capability it never claimed, because a
	// suite only we can pass would be a specification of our implementation
	// rather than of DeDi.
	Profiles []string `json:"profiles"`

	// Fixtures maps a role (see RequiredFixtures) to a concrete name in this
	// deployment's data.
	Fixtures map[string]string `json:"fixtures"`

	// PublisherDomain is the domain whose /.well-known/dedi.index.json the
	// publication profile verifies, e.g. "example.org". Required by, and only
	// by, that profile — it is a property of a publisher, not of a server,
	// and the two are separate roles in the standard.
	PublisherDomain string `json:"publisher_domain,omitempty"`

	// Headers are sent with every request — for an implementation that puts
	// its read plane behind a gateway. Optional, and never required by the
	// standard.
	Headers map[string]string `json:"headers,omitempty"`
}

// Fixture roles. Cases refer to these constants, never to a literal name, so
// that what a case needs is stated once and satisfied per deployment.
const (
	// Namespace is a namespace that exists and contains Registry.
	Namespace = "namespace"
	// Registry is a registry under Namespace that contains Record.
	Registry = "registry"
	// Record is a record under Registry that exists and is live.
	Record = "record"

	// RecordWithVersions is a record with at least two versions, so version
	// history and as_on time-travel can be exercised. Without it those cases
	// cannot distinguish a correct implementation from one that ignores the
	// parameter and always returns current.
	RecordWithVersions = "record_with_versions"

	// RevokedRecord is a record whose current state is revoked. The standard's
	// whole point is that a revocation is visible, so an implementation that
	// cannot show one has not demonstrated the property that matters.
	RevokedRecord = "revoked_record"

	// AbsentNamespace names a namespace that must NOT exist. It is a fixture
	// rather than a hardcoded string because "obviously absent" is not the
	// suite's call to make — on some deployment the string we invented might
	// be real, and the case would fail for the wrong reason.
	AbsentNamespace = "absent_namespace"
)

// requiredByProfile lists the fixture roles each profile's cases need. Loading
// validates against this, so a manifest is rejected up front with a list of
// what is missing rather than failing case by case halfway through a run.
var requiredByProfile = map[string][]string{
	ProfileCore:       {Namespace, Registry, Record, AbsentNamespace},
	ProfileVersioning: {Namespace, Registry, RecordWithVersions, RevokedRecord},
	ProfileBeckn:      {Namespace, Registry, Record},
	// Publication is about a publisher's domain, not a server's data: it
	// fetches the well-known manifest and verifies signatures. It needs
	// PublisherDomain rather than any record fixture, which Validate checks
	// separately.
	ProfilePublication: {},
}

// The profiles a manifest may claim.
const (
	// ProfileCore is the eight read endpoints of api/openapi.yaml.
	ProfileCore = "core"
	// ProfileVersioning is version history, as_on time-travel and revocation
	// visibility — the semantics the OpenAPI document declares parameters for
	// but assigns no meaning to.
	ProfileVersioning = "versioning"
	// ProfilePublication is docs/publishing-dedi-files.md: the signed
	// manifest at the normative well-known path, and the 5-step verification
	// its §7.3 makes mandatory. This is where the standard's own MUSTs are,
	// so it is the profile that most directly measures conformance.
	ProfilePublication = "publication"
	// ProfileBeckn is the Beckn subscriber resolution convention.
	ProfileBeckn = "beckn"
)

// KnownProfiles is every profile this suite implements, in report order.
var KnownProfiles = []string{ProfileCore, ProfileVersioning, ProfilePublication, ProfileBeckn}

// Claims reports whether the manifest opted into a profile.
func (m *Manifest) Claims(profile string) bool {
	for _, p := range m.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// Fixture returns the concrete name for a role. Validation at load time
// guarantees the roles a claimed profile needs are present, so a missing one
// here is a suite bug — a case asking for a role its profile never declared —
// and says so rather than silently testing the empty string.
func (m *Manifest) Fixture(role string) string {
	v, ok := m.Fixtures[role]
	if !ok {
		panic(fmt.Sprintf("conformance: case asked for fixture role %q, which no claimed profile declares it needs; "+
			"add it to requiredByProfile for the profile that owns the case", role))
	}
	return v
}

// Load reads and validates a manifest.
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // a typo'd key would otherwise silently do nothing
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

// Validate checks the manifest is self-consistent and complete for everything
// it claims. It reports every problem at once: fixing one, re-running, and
// discovering the next is a bad first experience for someone integrating.
func (m *Manifest) Validate() error {
	var probs []string

	// base_url addresses a server. A publisher-only manifest has no server to
	// address, so requiring it would exclude exactly the adopters the
	// standard says need no infrastructure at all.
	needsBaseURL := m.Claims(ProfileCore) || m.Claims(ProfileVersioning) || m.Claims(ProfileBeckn)
	switch u, err := url.Parse(m.BaseURL); {
	case m.BaseURL == "":
		if needsBaseURL {
			probs = append(probs, "base_url is required by the server profiles (core, versioning, beckn)")
		}
	case err != nil:
		probs = append(probs, fmt.Sprintf("base_url %q does not parse: %v", m.BaseURL, err))
	case u.Scheme != "http" && u.Scheme != "https":
		probs = append(probs, fmt.Sprintf("base_url %q must be http or https", m.BaseURL))
	case u.Host == "":
		probs = append(probs, fmt.Sprintf("base_url %q has no host", m.BaseURL))
	}

	if len(m.Profiles) == 0 {
		probs = append(probs, fmt.Sprintf("profiles is empty — claim at least one of %s", strings.Join(KnownProfiles, ", ")))
	}
	for _, p := range m.Profiles {
		if _, ok := requiredByProfile[p]; !ok {
			probs = append(probs, fmt.Sprintf("unknown profile %q — known profiles are %s", p, strings.Join(KnownProfiles, ", ")))
		}
	}
	// Core is the base the server profiles assume: they resolve read-API
	// paths that core is what proves work at all. Publication is exempt — a
	// publisher serves signed files from its own domain and need not operate
	// a read API, which the standard states explicitly ("No infrastructure
	// need be operated").
	serverProfile := m.Claims(ProfileVersioning) || m.Claims(ProfileBeckn)
	if serverProfile && !m.Claims(ProfileCore) {
		probs = append(probs, "the versioning and beckn profiles build on \"core\", so it must be claimed alongside them")
	}
	if m.Claims(ProfilePublication) && strings.TrimSpace(m.PublisherDomain) == "" {
		probs = append(probs, "the publication profile verifies a publisher's well-known manifest, so publisher_domain is required")
	}
	if !m.Claims(ProfileCore) && !m.Claims(ProfilePublication) && len(m.Profiles) > 0 {
		probs = append(probs, "claim at least one of \"core\" (a DeDi server) or \"publication\" (a DeDi publisher)")
	}

	need := map[string][]string{} // role -> profiles that need it
	for _, p := range m.Profiles {
		for _, role := range requiredByProfile[p] {
			need[role] = append(need[role], p)
		}
	}
	var missing []string
	for role, profiles := range need {
		if strings.TrimSpace(m.Fixtures[role]) == "" {
			sort.Strings(profiles)
			missing = append(missing, fmt.Sprintf("%s (needed by: %s)", role, strings.Join(profiles, ", ")))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		probs = append(probs, "missing fixtures:\n    - "+strings.Join(missing, "\n    - "))
	}

	if len(probs) > 0 {
		return fmt.Errorf("%s", strings.Join(probs, "\n  "))
	}
	return nil
}
