package suite

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/theflywheel/dedi-conformance/manifest"
)

// The core profile: the eight read endpoints the standard defines, the
// envelope they answer in, and the behaviour a client can rely on without
// knowing anything about how the implementation stores its data.
var coreCases = []namedCase{
	{"every spec path resolves", caseSpecPathsResolve},
	{"responses use the message/data envelope", caseEnvelope},
	{"a record lookup returns the fields a client resolves on", caseRecordFields},
	{"documented enum values are accepted", caseEnumParams},
	{"an absent namespace is 404, not an empty success", caseAbsentIs404},
	{"an error response carries a machine-readable code", caseErrorShape},
	{"a version_id the contract permits is not a bad request", caseVersionIDType},
	{"lookup is safe to repeat", caseIdempotentRead},
}

// resolvePath substitutes fixture values for the {params} in a spec path
// template.
func resolvePath(t *T, tmpl string) string {
	out := tmpl
	for _, seg := range strings.Split(tmpl, "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		var role string
		switch name := strings.Trim(seg, "{}"); name {
		case "namespace":
			role = manifest.Namespace
		case "registry_name":
			role = manifest.Registry
		case "record_name":
			role = manifest.Record
		default:
			t.Fatalf("the spec path %q has a parameter %q this suite has no fixture role for", tmpl, name)
		}
		out = strings.Replace(out, seg, url.PathEscape(t.Fixture(role)), 1)
	}
	return out
}

// caseSpecPathsResolve drives every path the spec declares, from the spec
// itself rather than a hardcoded list — so a spec update that adds an endpoint
// is exercised without editing this file.
func caseSpecPathsResolve(t *T) {
	t.SpecRef("api/openapi.yaml paths")
	for _, ep := range t.s.Spec.Endpoints() {
		if ep.Method != "GET" {
			t.Errorf("%s: this suite only knows how to exercise GET; the spec has grown a %s endpoint that needs new support",
				ep.Key(), ep.Method)
			continue
		}
		r := t.GET(resolvePath(t, ep.Path), nil)
		if r.Status != 200 {
			t.Errorf("%s: status %d, want 200 — with fixture values substituted this path names data the manifest says exists\n  %s",
				ep.Key(), r.Status, truncate(r.Body, 200))
		}
	}
}

// caseEnvelope checks the {message, data} wrapper every 200 response uses. It
// is the one shape assertion the spec really does make, since every 200
// response body in the document is an object with those two properties.
func caseEnvelope(t *T) {
	t.SpecRef("api/openapi.yaml — every 200 response is {message, data}")
	for _, ep := range t.s.Spec.Endpoints() {
		if ep.Method != "GET" {
			continue
		}
		r := t.GET(resolvePath(t, ep.Path), nil)
		if r.Status != 200 {
			continue // reported by caseSpecPathsResolve; not this case's claim
		}
		if r.JSON == nil {
			t.Errorf("%s: response body is not a JSON object: %s", ep.Key(), truncate(r.Body, 200))
			continue
		}
		for _, key := range []string{"message", "data"} {
			if _, ok := r.JSON[key]; !ok {
				t.Errorf("%s: response envelope has no %q key (got keys: %s)", ep.Key(), key, keysOf(r.JSON))
			}
		}
	}
}

// requiredRecordFields is this suite's own judgment, not a derivation.
//
// The spec's component schemas declare no `required` array anywhere, so
// nothing in the document makes any field mandatory. Taken literally, an
// implementation could answer a record lookup with `{"message": "ok", "data":
// {}}` and violate nothing — which cannot be what conformance means, since a
// client resolving an identity has no way to use that answer.
//
// These are the fields a reader must have for the response to do the job the
// standard describes: identify which record answered, say whether it is
// currently valid, and say which version of it this is. Anything beyond that
// is left alone deliberately — the suite should not invent obligations the
// standard does not imply.
var requiredRecordFields = []string{"record_name", "namespace", "registry_name", "state", "version"}

func caseRecordFields(t *T) {
	t.SpecRef("components.schemas.Record — see requiredRecordFields for why these five")
	path := fmt.Sprintf("/dedi/lookup/%s/%s/%s",
		url.PathEscape(t.Fixture(manifest.Namespace)),
		url.PathEscape(t.Fixture(manifest.Registry)),
		url.PathEscape(t.Fixture(manifest.Record)))
	r := t.GET(path, nil)
	t.wantStatus(r, 200)

	data, ok := r.Envelope()
	if !ok {
		t.Fatalf("no data in the envelope: %s", truncate(r.Body, 200))
	}
	obj, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want an object describing the record", data)
	}
	for _, f := range requiredRecordFields {
		if _, present := obj[f]; !present {
			t.Errorf("record lookup returned no %q — a client cannot act on this answer without it (got: %s)",
				f, keysOf(obj))
		}
	}
	// state is the one field the spec does constrain, so check the value and
	// not merely its presence.
	if v, ok := obj["state"].(string); ok && !contains(recordStates, v) {
		t.Errorf("state = %q, which is outside the spec's enum %v", v, recordStates)
	}
}

var recordStates = []string{"draft", "live", "suspended", "revoked", "expired"}

// caseEnumParams sends each documented enum parameter one of the spec's own
// values. This is the case that catches an implementation rejecting something
// the published contract permits — a real interoperability failure, because a
// client written against the spec will send exactly these.
func caseEnumParams(t *T) {
	t.SpecRef("api/openapi.yaml — query parameters with an enum")
	for _, ep := range t.s.Spec.Endpoints() {
		if ep.Method != "GET" {
			continue
		}
		for _, qp := range ep.QueryParams() {
			if !qp.HasEnum() {
				continue
			}
			for _, v := range qp.Schema.Enum {
				q := url.Values{}
				q.Set(qp.Name, v)
				r := t.GET(resolvePath(t, ep.Path), q)
				if r.Status != 200 {
					t.Errorf("%s?%s=%s: status %d, want 200 — the spec lists %q as a permitted value",
						ep.Key(), qp.Name, v, r.Status, v)
				}
			}
		}
	}
}

// caseAbsentIs404 pins the distinction between "no such thing" and "a thing
// with nothing in it". Answering 200 for an absent namespace is the failure
// that matters most in practice: a relying party checking whether an identity
// is registered would read it as a successful lookup.
func caseAbsentIs404(t *T) {
	t.SpecRef("api/openapi.yaml 404 — 'Namespace, registry, or record not found'")
	absent := t.Fixture(manifest.AbsentNamespace)
	r := t.GET("/dedi/lookup/"+url.PathEscape(absent), nil)
	if r.Status != 404 {
		t.Errorf("looking up the namespace %q, which the manifest declares does not exist, returned %d, want 404\n"+
			"a client cannot distinguish 'not registered' from 'registered but empty' if this succeeds\n  %s",
			absent, r.Status, truncate(r.Body, 200))
	}
}

// caseErrorShape checks an error is machine-readable. A client that must parse
// prose to tell "not found" from "malformed" cannot be written correctly.
func caseErrorShape(t *T) {
	t.SpecRef("components.schemas.ErrorResponse")
	r := t.GET("/dedi/lookup/"+url.PathEscape(t.Fixture(manifest.AbsentNamespace)), nil)
	if r.Status == 200 {
		t.Skipf("the absent-namespace lookup returned 200, so there is no error response to inspect; see the 404 case")
	}
	if r.JSON == nil {
		t.Fatalf("error response is not a JSON object: %s", truncate(r.Body, 200))
	}
	// The spec's ErrorResponse has message, error and code, none marked
	// required. `code` is the one a client branches on, so its absence is the
	// failure worth reporting.
	if _, ok := r.JSON["code"]; !ok {
		t.Errorf("error response carries no %q field, so the failure is not machine-readable (got: %s)",
			"code", keysOf(r.JSON))
	}
}

// caseVersionIDType is a contract case rather than a behaviour case.
//
// The spec declares version_id as `type: string` with no format and no
// pattern, so a non-numeric value is a well-formed request under the published
// contract. An implementation that stores integer version ids will not find
// it — but "I have no such version" is 404, whereas 400 tells the client its
// request was malformed when the spec says it was not.
//
// This was found by running an earlier form of this suite against our own
// implementation, which returned 400. We changed ours.
func caseVersionIDType(t *T) {
	t.SpecRef("api/openapi.yaml — version_id: {type: string}, no format, no pattern")
	q := url.Values{}
	q.Set("version_id", "not-an-integer-but-a-valid-spec-string")
	r := t.GET("/dedi/lookup/"+url.PathEscape(t.Fixture(manifest.Namespace)), q)
	if r.Status == 400 {
		t.Errorf("status 400 for a version_id the published contract permits (type: string, unconstrained)\n" +
			"either tighten the spec's type or treat an unrecognised version_id as 404 (no such version)")
	}
}

// caseIdempotentRead checks a lookup repeated immediately gives the same
// answer. Reads are the whole standard; one that is not repeatable makes every
// other guarantee unverifiable, because two parties checking the same identity
// could legitimately disagree.
func caseIdempotentRead(t *T) {
	t.SpecRef("GET semantics (RFC 9110 §9.2.1) — lookup is a safe method")
	path := fmt.Sprintf("/dedi/lookup/%s/%s/%s",
		url.PathEscape(t.Fixture(manifest.Namespace)),
		url.PathEscape(t.Fixture(manifest.Registry)),
		url.PathEscape(t.Fixture(manifest.Record)))
	first := t.GET(path, nil)
	t.wantStatus(first, 200)
	second := t.GET(path, nil)
	t.wantStatus(second, 200)

	a, _ := first.Envelope()
	b, _ := second.Envelope()
	fa, _ := a.(map[string]any)
	fb, _ := b.(map[string]any)
	// Compare only the fields that identify the record and its version:
	// timestamps and TTL counters legitimately move between two calls.
	for _, f := range []string{"record_name", "namespace", "registry_name", "state", "version"} {
		if fmt.Sprint(fa[f]) != fmt.Sprint(fb[f]) {
			t.Errorf("two identical lookups disagreed on %q: %v then %v", f, fa[f], fb[f])
		}
	}
}

func keysOf(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	if len(ks) == 0 {
		return "(none)"
	}
	return strings.Join(ks, ", ")
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
