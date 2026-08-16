package suite

import (
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/theflywheel/dedi-conformance/internal/manifest"
)

// The versioning profile.
//
// These are the cases the OpenAPI document cannot express. The spec declares
// that `as_on` and `version_id` exist and gives their types; it says nothing
// about what they must mean. An implementation that parsed both and ignored
// both would satisfy every schema assertion in the core profile.
//
// That matters more here than it looks. The standard's value proposition is
// that a relying party can establish what a directory said at a point in time
// and see that a revocation happened — so an implementation that answers only
// "here is the current value" has not implemented the interesting half.
var versioningCases = []namedCase{
	{"a record reports its version history", caseVersionHistory},
	{"as_on returns the version that was live at that instant", caseAsOnTimeTravel},
	{"a revoked record is still resolvable and says it is revoked", caseRevokedVisible},
	{"history is append-only under repeated reads", caseHistoryStable},
	{"as_on before the record existed is not a silent current-value answer", caseAsOnBeforeGenesis},
}

func recordPath(t *T, role string) string {
	return fmt.Sprintf("/dedi/lookup/%s/%s/%s",
		url.PathEscape(t.Fixture(manifest.Namespace)),
		url.PathEscape(t.Fixture(manifest.Registry)),
		url.PathEscape(t.Fixture(role)))
}

func versionsPath(t *T, role string) string {
	return fmt.Sprintf("/dedi/versions/%s/%s/%s",
		url.PathEscape(t.Fixture(manifest.Namespace)),
		url.PathEscape(t.Fixture(manifest.Registry)),
		url.PathEscape(t.Fixture(role)))
}

// versionList pulls the version ids out of a /dedi/versions response.
//
// The spec fixes this shape precisely — data.versions is `array of string`,
// version ids and nothing else. Note what that means for this profile: the
// history carries no timestamps, so "which version was live at time T" cannot
// be answered from the history alone. The as_on case therefore establishes a
// version's own instant by looking that version up, which is the only route
// the standard actually provides.
func versionList(t *T, r *Response) []string {
	data, ok := r.Envelope()
	if !ok {
		t.Fatalf("no data in the envelope: %s", truncate(r.Body, 200))
	}
	obj, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("versions data is %T, want an object with a %q array", data, "versions")
	}
	arr, ok := obj["versions"].([]any)
	if !ok {
		t.Fatalf("the versions response carries no %q array (got: %s)", "versions", keysOf(obj))
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		switch v := e.(type) {
		case string:
			out = append(out, v)
		case float64:
			out = append(out, fmt.Sprint(int64(v)))
		default:
			t.Errorf("a version id is %T, but the spec declares versions as an array of string", e)
		}
	}
	return out
}

// versionAt looks a specific version up and returns it with the instant it was
// created. This is how the profile gets timestamps at all, since the history
// endpoint does not carry them.
func versionAt(t *T, role, versionID string) (map[string]any, time.Time) {
	q := url.Values{}
	q.Set("version_id", versionID)
	r := t.GET(recordPath(t, role), q)
	if r.Status != 200 {
		t.Skipf("version %q, which the history endpoint listed, could not be looked up (status %d) — "+
			"without it there is no way to establish when that version was live", versionID, r.Status)
	}
	obj, ok := mustObject(t, r)
	if !ok {
		t.Skipf("version %q did not return a record object", versionID)
	}
	// updated_at first, deliberately. On a versioned record created_at is the
	// record's genesis and is identical across every version, so reading it
	// here makes all versions look simultaneous — which silently turns the
	// as_on case into a skip rather than a check.
	return obj, firstTime(obj, "updated_at", "valid_from", "created_at")
}

func caseVersionHistory(t *T) {
	t.SpecRef("/dedi/versions/{namespace}/{registry_name}/{record_name}")
	r := t.GET(versionsPath(t, manifest.RecordWithVersions), nil)
	t.wantStatus(r, 200)

	versions := versionList(t, r)
	if len(versions) < 2 {
		t.Errorf("the record named by record_with_versions reported %d version(s)\n"+
			"this fixture must name a record that has been updated at least once, or the profile "+
			"cannot distinguish an implementation that keeps history from one that does not", len(versions))
	}
}

// caseAsOnTimeTravel is the load-bearing case of this profile. It reads the
// record's history, picks a moment strictly inside it, and requires the answer
// to be the version that was live then — not the current one.
func caseAsOnTimeTravel(t *T) {
	t.SpecRef("as_on: {type: string, format: date-time} on the lookup endpoints")

	hist := t.GET(versionsPath(t, manifest.RecordWithVersions), nil)
	t.wantStatus(hist, 200)
	ids := versionList(t, hist)
	if len(ids) < 2 {
		t.Skipf("needs a record with at least two versions; record_with_versions has %d", len(ids))
	}

	// Establish each version's instant by looking it up — the history endpoint
	// carries ids only.
	type v struct {
		id string
		at time.Time
	}
	var vs []v
	for _, id := range ids {
		obj, at := versionAt(t, manifest.RecordWithVersions, id)
		_ = obj
		if !at.IsZero() {
			vs = append(vs, v{id, at})
		}
	}
	if len(vs) < 2 {
		t.Skipf("could not establish creation instants for two versions; got %d of %d", len(vs), len(ids))
	}
	// Compare *adjacent* versions, not oldest against newest. With three or
	// more versions the moment just before the newest is one at which the
	// second-newest was live — expecting the oldest there would fail a
	// correct implementation for being correct.
	sort.Slice(vs, func(i, j int) bool { return vs[i].at.Before(vs[j].at) })
	prev, last := vs[len(vs)-2], vs[len(vs)-1]
	if !prev.at.Before(last.at) || prev.id == last.id {
		t.Skipf("the two most recent versions do not span two distinct instants, so there is no " +
			"moment at which the answer should differ from current")
	}

	// A moment after prev became live but before last replaced it. The answer
	// must be prev.
	at := last.at.Add(-1 * time.Second)
	if !at.After(prev.at) {
		at = prev.at.Add(last.at.Sub(prev.at) / 2)
	}
	oldest, newest := prev, last
	q := url.Values{}
	q.Set("as_on", at.UTC().Format(time.RFC3339))
	r := t.GET(recordPath(t, manifest.RecordWithVersions), q)
	t.wantStatus(r, 200)

	got, _ := r.Envelope()
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("as_on lookup returned %T, want a record object", got)
	}
	gotID := firstString(obj, "version_id", "version", "id")
	if gotID == newest.id {
		t.Errorf("as_on=%s returned version %q, which only became live at %s\n"+
			"the version live at that instant was %q (created %s)\n"+
			"an implementation that accepts as_on and always answers with current cannot be used "+
			"to establish what this directory said at a past moment",
			q.Get("as_on"), gotID, newest.at.UTC().Format(time.RFC3339), oldest.id,
			oldest.at.UTC().Format(time.RFC3339))
	} else if gotID != oldest.id {
		t.Errorf("as_on=%s returned version %q, want %q (the version live at that instant)",
			q.Get("as_on"), gotID, oldest.id)
	}
}

// caseRevokedVisible: a revocation must be a state a reader can see, not a
// deletion. If a revoked record 404s, a relying party cannot tell "this key
// was revoked" from "this key was never registered" — and those call for
// opposite responses.
func caseRevokedVisible(t *T) {
	t.SpecRef("components.schemas.Record.state — enum includes 'revoked'")
	r := t.GET(recordPath(t, manifest.RevokedRecord), nil)
	if r.Status == 404 {
		t.Fatalf("the revoked record is 404\n" +
			"a revocation that removes the record is indistinguishable from a record that never " +
			"existed, and those two facts call for opposite decisions by a relying party")
	}
	t.wantStatus(r, 200)

	obj, ok := mustObject(t, r)
	if !ok {
		return
	}
	state, _ := obj["state"].(string)
	if state != "revoked" {
		t.Errorf("the record named by revoked_record reports state %q, want \"revoked\"", state)
	}
}

func caseHistoryStable(t *T) {
	t.SpecRef("append-only history")
	first := versionList(t, t.GET(versionsPath(t, manifest.RecordWithVersions), nil))
	second := versionList(t, t.GET(versionsPath(t, manifest.RecordWithVersions), nil))
	if len(second) < len(first) {
		t.Errorf("version history shrank between two reads: %d then %d entries\n"+
			"history is append-only; an entry that can disappear cannot be cited as evidence",
			len(first), len(second))
	}
}

// caseAsOnBeforeGenesis: asking for a moment before the record existed has one
// correct answer — there was nothing — and one dangerous wrong one: returning
// the current value, which asserts the record existed when it did not.
func caseAsOnBeforeGenesis(t *T) {
	t.SpecRef("as_on semantics")
	q := url.Values{}
	q.Set("as_on", "1990-01-01T00:00:00Z")
	r := t.GET(recordPath(t, manifest.RecordWithVersions), q)
	switch {
	case r.Status == 404:
		// Correct: nothing was live then.
	case r.Status == 400:
		// Also defensible: the implementation rejects a time before its
		// history. The client is not misled either way.
	case r.Status == 200:
		t.Errorf("as_on=1990-01-01T00:00:00Z returned 200\n" +
			"answering with a record for a moment before it existed asserts something false; " +
			"want 404 (nothing was live then)")
	default:
		t.Errorf("as_on far in the past returned %d; want 404", r.Status)
	}
}

func mustObject(t *T, r *Response) (map[string]any, bool) {
	data, ok := r.Envelope()
	if !ok {
		t.Errorf("no data in the envelope: %s", truncate(r.Body, 200))
		return nil, false
	}
	obj, ok := data.(map[string]any)
	if !ok {
		t.Errorf("data is %T, want an object", data)
		return nil, false
	}
	return obj, true
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return fmt.Sprint(v)
		}
	}
	return ""
}

func firstTime(m map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		s, ok := m[k].(string)
		if !ok {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999Z07:00"} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}
