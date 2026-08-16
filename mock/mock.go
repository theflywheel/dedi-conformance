// Package mock serves a DeDi read API for testing the conformance suite
// itself.
//
// The point is the defects. A conformance suite's characteristic failure is
// not a false alarm — it is passing everything, quietly, because an assertion
// stopped asserting. Nothing about a green report reveals that. So this
// package can serve a deliberately non-conformant node, one defect at a time,
// and the suite's own tests require each defect to be caught by a named case.
//
// That turns "the suite works" from a claim into something with evidence
// behind it, and means an assertion cannot be weakened without a test going
// red.
package mock

import (
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// Defects a mock node can be given.
type Defect string

const (
	// None is the conformant node.
	None Defect = ""
	// AbsentIs200 answers a lookup for a namespace that does not exist with
	// 200 and empty data — the failure that makes "not registered"
	// indistinguishable from "registered but empty".
	AbsentIs200 Defect = "absent-is-200"
	// NoEnvelope returns the payload unwrapped, without {message, data}.
	NoEnvelope Defect = "no-envelope"
	// MissingRecordFields omits the fields a client resolves on.
	MissingRecordFields Defect = "missing-record-fields"
	// RejectsSpecEnum 400s on a query value the spec lists as permitted.
	RejectsSpecEnum Defect = "rejects-spec-enum"
	// VersionIDIs400 rejects a version_id the published contract permits.
	VersionIDIs400 Defect = "version-id-400"
	// RevokedIsErased deletes on revocation instead of recording it: the
	// record resolves 404 AND its history is gone, leaving nothing to
	// distinguish it from a record that never existed. Note that 404 on the
	// live-binding read alone is NOT a defect — see caseRevokedVisible.
	RevokedIsErased Defect = "revoked-is-erased"
	// AsOnIgnored accepts as_on and always answers with the current version.
	AsOnIgnored Defect = "as-on-ignored"
	// NoErrorCode returns errors as prose with no machine-readable code.
	NoErrorCode Defect = "no-error-code"
	// BadState reports a record state outside the spec's enum.
	BadState Defect = "bad-state"
	// BadSubscriberType reports a Beckn subscriber role outside the reference
	// schema's enum, e.g. "PROVIDER". A counterparty routes on this value, so
	// an unrecognised role makes the record unusable even when every field is
	// present.
	BadSubscriberType Defect = "bad-subscriber-type"
	// NoCountries omits a field the Beckn_subscriber schema marks required.
	NoCountries Defect = "no-countries"
)

// Fixture names this mock serves. They are what a manifest pointed at this
// node must declare.
const (
	Namespace          = "acme"
	Registry           = "participants"
	Record             = "bap.acme.example"
	RecordWithVersions = "rotated.acme.example"
	RevokedRecord      = "gone.acme.example"
	AbsentNamespace    = "no-such-namespace-9f3a"
)

// Timestamps for the two versions of RecordWithVersions. Fixed rather than
// relative so a test's expectations do not drift with the clock.
var (
	v1At = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	v2At = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Server starts a mock DeDi node exhibiting the given defects (none for a
// conformant one). It is closed via t.Cleanup by the caller.
func Server(defects ...Defect) *httptest.Server {
	has := map[Defect]bool{}
	for _, d := range defects {
		has[d] = true
	}
	m := &node{defects: has}
	return httptest.NewServer(m)
}

type node struct{ defects map[Defect]bool }

func (n *node) has(d Defect) bool { return n.defects[d] }

func (n *node) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "dedi" {
		n.fail(w, http.StatusNotFound, "no such path")
		return
	}
	switch parts[1] {
	case "lookup":
		n.lookup(w, r, parts[2:])
	case "query":
		n.query(w, r, parts[2:])
	case "versions":
		n.versions(w, r, parts[2:])
	default:
		n.fail(w, http.StatusNotFound, "no such path")
	}
}

func (n *node) ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if n.has(NoEnvelope) {
		json.NewEncoder(w).Encode(data)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"message": "ok", "data": data})
}

func (n *node) fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"message": msg, "error": msg, "code": "NOT_FOUND"}
	if status == http.StatusBadRequest {
		body["code"] = "INVALID_REQUEST"
	}
	if n.has(NoErrorCode) {
		delete(body, "code")
	}
	json.NewEncoder(w).Encode(body)
}

// record builds a Record response body. created_at is the instant *this
// version* came into being, which is what lets a client establish when a
// version was live given that the history endpoint carries only ids.
func (n *node) record(name, version, state string) map[string]any {
	// created_at is the record's genesis and is the same on every version;
	// updated_at is the instant *this* version came to be. That is what a
	// client has to work with to place a version in time, since the history
	// endpoint carries ids only.
	updatedAt := v2At
	if version == "1" {
		updatedAt = v1At
	}
	rec := map[string]any{
		"record_id": "rec-" + name, "record_name": name,
		"registry_id": "reg-1", "registry_name": Registry,
		"namespace_id": "ns-1", "namespace": Namespace,
		"version": version, "version_count": 2,
		"state": state, "ttl": 300,
		"created_at": v1At.Format(time.RFC3339), "updated_at": updatedAt.Format(time.RFC3339),
		"details": map[string]any{
			"subscriber_id": name, "url": "https://" + name + "/beckn",
			"type": "BAP", "domain": "retail", "countries": []string{"IND"},
			"signing_public_key": "TZ8xHnQm2vN0pWq7RsYkLbCd4FgHjKlMnOpQrStUvWx=",
			"encr_public_key":    "aB3dEf5GhI7jKlM9nOpQrS1tUvW3xYz5AbC7dEf9GhI=",
		},
	}
	if n.has(MissingRecordFields) {
		delete(rec, "state")
		delete(rec, "version")
		delete(rec, "registry_name")
	}
	if n.has(BadState) {
		rec["state"] = "decommissioned" // outside the spec's enum
	}
	if det, ok := rec["details"].(map[string]any); ok {
		if n.has(BadSubscriberType) {
			det["type"] = "PROVIDER" // not one of BAP/BPP/BG/CDS
		}
		if n.has(NoCountries) {
			delete(det, "countries")
		}
	}
	return rec
}

func (n *node) lookup(w http.ResponseWriter, r *http.Request, p []string) {
	q := r.URL.Query()

	// A version_id the contract permits (type: string, unconstrained).
	if v := q.Get("version_id"); v != "" && !isInt(v) {
		if n.has(VersionIDIs400) {
			n.fail(w, http.StatusBadRequest, "version_id must be an integer version id")
			return
		}
		n.fail(w, http.StatusNotFound, "no such version")
		return
	}
	// An explicit version_id names a version directly.
	if v := q.Get("version_id"); v != "" && len(p) == 3 && p[2] == RecordWithVersions {
		if v != "1" && v != "2" {
			n.fail(w, http.StatusNotFound, "no such version")
			return
		}
		n.ok(w, n.record(p[2], v, "live"))
		return
	}

	if len(p) == 0 {
		n.fail(w, http.StatusNotFound, "no namespace given")
		return
	}
	if p[0] != Namespace {
		if n.has(AbsentIs200) {
			n.ok(w, map[string]any{})
			return
		}
		n.fail(w, http.StatusNotFound, "no such namespace")
		return
	}

	switch len(p) {
	case 1:
		n.ok(w, map[string]any{"namespace_id": "ns-1", "namespace": Namespace, "registry_count": 1})
		return
	case 2:
		if p[1] != Registry {
			n.fail(w, http.StatusNotFound, "no such registry")
			return
		}
		n.ok(w, map[string]any{"registry_id": "reg-1", "registry_name": Registry,
			"namespace": Namespace, "state": "live", "record_count": 3})
		return
	}

	name := p[2]
	switch name {
	case Record:
		n.ok(w, n.record(name, "2", "live"))
	case RevokedRecord:
		if n.has(RevokedIsErased) {
			n.fail(w, http.StatusNotFound, "no such record")
			return
		}
		n.ok(w, n.record(name, "2", "revoked"))
	case RecordWithVersions:
		version := "2"
		if asOn := q.Get("as_on"); asOn != "" && !n.has(AsOnIgnored) {
			at, err := time.Parse(time.RFC3339, asOn)
			if err != nil {
				n.fail(w, http.StatusBadRequest, "as_on must be RFC 3339")
				return
			}
			switch {
			case at.Before(v1At):
				n.fail(w, http.StatusNotFound, "nothing was live at that instant")
				return
			case at.Before(v2At):
				version = "1"
			}
		}
		n.ok(w, n.record(name, version, "live"))
	default:
		n.fail(w, http.StatusNotFound, "no such record")
	}
}

func (n *node) query(w http.ResponseWriter, r *http.Request, p []string) {
	q := r.URL.Query()
	// The spec's enums: status=[active,inactive], sort=[date,status,name,id],
	// state=[live]. A conformant node accepts each.
	for _, param := range []string{"status", "sort", "state"} {
		if v := q.Get(param); v != "" && n.has(RejectsSpecEnum) {
			n.fail(w, http.StatusBadRequest, "unsupported "+param+"="+v)
			return
		}
	}
	if len(p) == 0 || p[0] != Namespace {
		n.fail(w, http.StatusNotFound, "no such namespace")
		return
	}
	if len(p) == 1 {
		n.ok(w, map[string]any{"namespace": Namespace,
			"registries": []any{map[string]any{"registry_name": Registry, "state": "live"}}})
		return
	}
	n.ok(w, map[string]any{"namespace": Namespace, "registry_name": Registry,
		"records": []any{map[string]any{"record_name": Record, "state": "live"}}})
}

func (n *node) versions(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) == 0 || p[0] != Namespace {
		n.fail(w, http.StatusNotFound, "no such namespace")
		return
	}
	// The spec fixes this shape: data.versions is an array of *string* version
	// ids, with no timestamps. Serving objects here would let a suite bug —
	// reading a shape the standard does not define — pass unnoticed.
	if len(p) < 3 {
		n.ok(w, map[string]any{"namespace": Namespace, "total_versions": 1, "versions": []any{"1"}})
		return
	}
	if p[2] == RevokedRecord && n.has(RevokedIsErased) {
		n.fail(w, http.StatusNotFound, "no such record")
		return
	}
	if p[2] != RecordWithVersions {
		n.ok(w, map[string]any{"record_name": p[2], "total_versions": 1, "versions": []any{"2"}})
		return
	}
	n.ok(w, map[string]any{
		"record_name": p[2], "total_versions": 2,
		"created_at": v1At.Format(time.RFC3339), "updated_at": v2At.Format(time.RFC3339),
		"versions": []any{"1", "2"},
	})
}

func isInt(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
