package suite

import (
	"fmt"
	"net/url"

	"github.com/theflywheel/dedi-conformance/manifest"
	"github.com/theflywheel/dedi-conformance/openapi"
)

// The beckn profile: the standard's Beckn_subscriber reference registry.
//
// Scope note, because it would be easy to overreach here. The DeDi standard
// ships schemas/Beckn_subscriber.json as a *reference registry schema* — a
// shape a registry may adopt so that two implementations serving "beckn
// subscribers" agree on what a subscriber record contains. That schema, and
// its required fields, are in the standard, so they are testable.
//
// What is NOT in the standard is the resolution convention ONIX uses — the
// wildcard registry name "subscribers.beckn.one" appears nowhere in the spec.
// Asserting it here would make this suite a specification of the Beckn
// ecosystem's deployment habits rather than of DeDi, and would fail
// implementations that are conformant. So this profile checks the record
// shape, and stops there.
var becknCases = []namedCase{
	{"a subscriber record carries the reference schema's required fields", caseBecknSubscriberShape},
	{"a subscriber's type is one the reference schema permits", caseBecknSubscriberType},
}

// becknSchema is schemas/Beckn_subscriber.json, read at run time. Nothing
// about the subscriber shape is transcribed into this file: see
// openapi.JSONSchema for why a hand-copied enum is a trap.
func becknSchema(t *T) *openapi.JSONSchema {
	s, err := openapi.LoadJSONSchema(t.s.SpecPath, "Beckn_subscriber.json")
	if err != nil {
		t.Skipf("%v", err)
	}
	return s
}

// subscriberDetails pulls the record's payload. The read API nests the
// record's own fields under "details" (components.schemas.Record), but an
// implementation may also return them flat, so try both before failing —
// the standard does not settle it.
func subscriberDetails(t *T) map[string]any {
	path := fmt.Sprintf("/dedi/lookup/%s/%s/%s",
		url.PathEscape(t.Fixture(manifest.Namespace)),
		url.PathEscape(t.Fixture(manifest.Registry)),
		url.PathEscape(t.Fixture(manifest.Record)))
	r := t.GET(path, nil)
	t.wantStatus(r, 200)

	obj, ok := mustObject(t, r)
	if !ok {
		t.Fatalf("could not read the record")
	}
	if d, ok := obj["details"].(map[string]any); ok && len(d) > 0 {
		return d
	}
	return obj
}

func caseBecknSubscriberShape(t *T) {
	t.SpecRef("schemas/Beckn_subscriber.json — required")
	sch := becknSchema(t)
	d := subscriberDetails(t)
	for _, f := range sch.Required {
		if _, ok := d[f]; !ok {
			t.Errorf("the subscriber record has no %q, which the reference schema marks required "+
				"(got: %s)\nif this record is not a Beckn subscriber, point the record fixture at "+
				"one or drop the beckn profile", f, keysOf(d))
		}
	}
	// A subscriber whose signing key is absent or empty cannot be transacted
	// with at all, which is the one field the whole registry exists to carry.
	if k, ok := d["signing_public_key"].(string); ok && k == "" {
		t.Errorf("signing_public_key is present but empty; the record identifies no key to verify against")
	}
}

func caseBecknSubscriberType(t *T) {
	t.SpecRef("schemas/Beckn_subscriber.json — type.enum")
	types := becknSchema(t).EnumOf("type")
	if len(types) == 0 {
		t.Skipf("the reference schema constrains no %q values to check against", "type")
	}
	d := subscriberDetails(t)
	v, ok := d["type"].(string)
	if !ok {
		t.Skipf("no %q field to check; reported by the shape case", "type")
	}
	if !contains(types, v) {
		t.Errorf("subscriber type %q is not one of %v\n"+
			"a counterparty routes on this value, so an unrecognised role makes the record unusable "+
			"even though every other field is present", v, types)
	}
}
