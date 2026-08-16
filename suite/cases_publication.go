package suite

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

// The publication profile: docs/publishing-dedi-files.md.
//
// This is the profile that measures conformance most directly, because it is
// the only part of the standard written in MUSTs. The OpenAPI document
// describes an interface; §7.3 of the publication model prescribes a
// verification a conformant party must perform, in order, and states what it
// must refuse.
//
// So these cases do not merely check that a manifest is present and
// well-shaped. They perform the verification the standard mandates — JCS
// canonicalization, detached JWS over the document minus its proof block — and
// fail when a signature does not actually verify. A profile that checked only
// for the presence of a `proof` field would pass a publisher whose signatures
// are meaningless, which is precisely the failure the signature exists to
// prevent.
var publicationCases = []namedCase{
	{"the manifest is served at the normative well-known path", caseManifestAtWellKnown},
	{"the manifest carries every field its schema requires", caseManifestShape},
	{"the manifest declares JCS canonicalization", caseManifestJCS},
	{"proof.verification_method names a key the manifest lists", caseVerificationMethodResolves},
	{"the manifest's signature verifies against its own declared key", caseManifestSignatureVerifies},
	{"the manifest is fresh (now <= next_update)", caseManifestFreshness},
	{"a tampered manifest does not verify", caseTamperIsDetected},
}

const wellKnownPath = "/.well-known/dedi.index.json"

// fetchManifest gets the publisher's well-known document. Cases share it via
// this helper rather than a cached global, so each case reports the request it
// actually made.
func fetchManifest(t *T) map[string]any {
	domain := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(t.s.M.PublisherDomain, "https://"), "http://"), "/")
	u := "https://" + domain + wellKnownPath
	t.request = "GET " + u

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("could not build a request for %s: %v", u, err)
	}
	resp, err := t.s.Client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v\nthe manifest path is normative (RFC 8615), so it must be reachable "+
			"under the domain's own TLS", u, err)
	}
	defer resp.Body.Close()
	body := make([]byte, 1<<20)
	n, _ := readFull(resp.Body, body)
	body = body[:n]

	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d, want 200\n%s", u, resp.StatusCode, truncate(body, 300))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("the manifest is not a JSON object: %v\n%s", err, truncate(body, 300))
	}
	return m
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
	}
	return total, nil
}

func caseManifestAtWellKnown(t *T) {
	t.SpecRef("publishing-dedi-files.md §5 — /.well-known/dedi.index.json, fixed path, normative (RFC 8615)")
	m := fetchManifest(t)
	if len(m) == 0 {
		t.Errorf("the manifest is an empty object")
	}
}

// manifestRequired mirrors schemas/dedi-manifest.schema.json's own `required`
// array. Unlike the OpenAPI document, the JSON Schemas do state what is
// mandatory, so this list is derived from the standard rather than chosen.
var manifestRequired = []string{"dedi_version", "domain", "keys", "updated_at", "next_update", "files", "proof"}

func caseManifestShape(t *T) {
	t.SpecRef("schemas/dedi-manifest.schema.json — required")
	m := fetchManifest(t)
	for _, f := range manifestRequired {
		if _, ok := m[f]; !ok {
			t.Errorf("the manifest has no %q, which its schema marks required (got: %s)", f, keysOf(m))
		}
	}
	// keys has minItems: 1 — a manifest declaring no key can authenticate
	// nothing, so an empty array is worse than a missing field.
	if keys, ok := m["keys"].([]any); ok && len(keys) == 0 {
		t.Errorf("the manifest declares an empty %q array; its schema requires at least one key, "+
			"and with none no file can ever be authenticated against this domain", "keys")
	}
	// The manifest's own domain must be the one it is served from, or a file
	// could cite a manifest that never claimed it.
	if d, ok := m["domain"].(string); ok {
		want := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(t.s.M.PublisherDomain, "https://"), "http://"), "/")
		if !strings.EqualFold(d, want) {
			t.Errorf("the manifest served at %s declares domain %q\n"+
				"a manifest that names a domain other than the one serving it cannot authenticate "+
				"that domain's files", want+wellKnownPath, d)
		}
	}
}

func proofOf(t *T, doc map[string]any) map[string]any {
	p, ok := doc["proof"].(map[string]any)
	if !ok {
		t.Fatalf("the document has no %q block, so nothing about it can be verified; signing is mandatory "+
			"(publishing-dedi-files.md §7)", "proof")
	}
	return p
}

func caseManifestJCS(t *T) {
	t.SpecRef("publishing-dedi-files.md §7.1 — proof.canonicalization MUST be \"JCS\"")
	p := proofOf(t, fetchManifest(t))
	if c, _ := p["canonicalization"].(string); c != "JCS" {
		t.Errorf("proof.canonicalization = %q, MUST be \"JCS\"\n"+
			"without a fixed canonicalization the same document re-serialized verifies differently, "+
			"which makes the signature unusable by anyone who did not receive the exact bytes", c)
	}
}

func caseVerificationMethodResolves(t *T) {
	t.SpecRef("publishing-dedi-files.md §7.2 — proof.verification_method MUST name the signing key's kid")
	m := fetchManifest(t)
	p := proofOf(t, m)
	vm, _ := p["verification_method"].(string)
	if vm == "" {
		t.Fatalf("proof.verification_method is absent or empty, so the proof does not say which key signed it")
	}
	if _, ok := findKey(m, vm); !ok {
		t.Errorf("proof.verification_method is %q, which is not the kid of any key in the manifest's %q\n"+
			"the proof names a key the document itself does not declare, so it cannot be checked", vm, "keys")
	}
}

// findKey returns the JWK in the manifest whose kid matches.
func findKey(m map[string]any, kid string) (map[string]any, bool) {
	keys, _ := m["keys"].([]any)
	for _, k := range keys {
		jwk, ok := k.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := jwk["kid"].(string); s == kid {
			return jwk, true
		}
	}
	return nil, false
}

// verifyDoc performs the standard's step 2: canonicalize the document with the
// proof block removed, then verify the detached JWS against the named key.
//
// Returns a human-readable reason on failure, "" on success.
func verifyDoc(doc map[string]any) string {
	proof, ok := doc["proof"].(map[string]any)
	if !ok {
		return "no proof block"
	}
	jwsVal, _ := proof["jws"].(string)
	if jwsVal == "" {
		return "proof.jws is absent or empty"
	}
	kid, _ := proof["verification_method"].(string)
	jwk, ok := findKey(doc, kid)
	if !ok {
		return fmt.Sprintf("proof.verification_method %q names no key in keys[]", kid)
	}

	// Signing input: the whole document minus proof, JCS-canonicalized.
	stripped := make(map[string]any, len(doc))
	for k, v := range doc {
		if k != "proof" {
			stripped[k] = v
		}
	}
	raw, err := json.Marshal(stripped)
	if err != nil {
		return fmt.Sprintf("could not re-serialize the document: %v", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return fmt.Sprintf("the document does not JCS-canonicalize: %v", err)
	}

	pub, reason := ed25519Key(jwk)
	if reason != "" {
		return reason
	}

	// Detached JWS (RFC 7515): header..signature, with the payload supplied
	// out of band — here, the canonical bytes.
	parts := strings.Split(jwsVal, ".")
	if len(parts) != 3 {
		return fmt.Sprintf("proof.jws is not a three-part JWS (got %d segments)", len(parts))
	}
	if parts[1] != "" {
		return "proof.jws carries an inline payload; §7.2 requires a detached JWS"
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Sprintf("the JWS signature is not base64url: %v", err)
	}
	signingInput := append([]byte(parts[0]+"."), canonical...)
	if !ed25519.Verify(pub, signingInput, sig) {
		return "the signature does not verify over the JCS-canonicalized document minus its proof block"
	}
	return ""
}

// ed25519Key extracts an Ed25519 public key from an RFC 7517 JWK.
func ed25519Key(jwk map[string]any) (ed25519.PublicKey, string) {
	kty, _ := jwk["kty"].(string)
	crv, _ := jwk["crv"].(string)
	if kty != "OKP" || crv != "Ed25519" {
		return nil, fmt.Sprintf("this suite verifies Ed25519 (kty=OKP, crv=Ed25519); the key declares kty=%q crv=%q", kty, crv)
	}
	x, _ := jwk["x"].(string)
	b, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil {
		return nil, fmt.Sprintf("the key's x is not base64url: %v", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Sprintf("the key's x decodes to %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), ""
}

// caseManifestSignatureVerifies is the case this profile exists for.
func caseManifestSignatureVerifies(t *T) {
	t.SpecRef("publishing-dedi-files.md §7.3 step 2 — integrity, offline")
	m := fetchManifest(t)
	if reason := verifyDoc(m); reason != "" {
		if strings.Contains(reason, "this suite verifies Ed25519") {
			t.Skipf("%s — the standard permits other algorithms; this suite implements Ed25519 only", reason)
		}
		t.Errorf("the manifest's signature does not verify: %s\n"+
			"every DeDi file's authenticity chains to this signature, so a manifest that does not "+
			"verify makes the publisher's entire directory unauthenticatable", reason)
	}
}

// caseTamperIsDetected is the meta-case: it flips a byte of the document and
// requires that verification now fails. It guards against the way this profile
// could be silently useless — a verifyDoc that returns "" for everything would
// pass every case above while checking nothing.
func caseTamperIsDetected(t *T) {
	t.SpecRef("publishing-dedi-files.md §7.3 step 2 — 'Reject on failure'")
	m := fetchManifest(t)
	if reason := verifyDoc(m); reason != "" {
		t.Skipf("the untampered manifest does not verify, so this case cannot distinguish " +
			"tamper-detection from that prior failure")
	}
	tampered := make(map[string]any, len(m))
	for k, v := range m {
		tampered[k] = v
	}
	tampered["domain"] = "attacker.example"
	if reason := verifyDoc(tampered); reason == "" {
		t.Errorf("a manifest with its %q changed to %q still verified\n"+
			"the signature does not actually bind the document's contents, so any party could "+
			"restate this publisher's directory as their own", "domain", "attacker.example")
	}
}

func caseManifestFreshness(t *T) {
	t.SpecRef("publishing-dedi-files.md §7.3 step 4 — freshness")
	m := fetchManifest(t)
	s, _ := m["next_update"].(string)
	if s == "" {
		t.Fatalf("the manifest has no %q, so a verifier cannot tell when to re-fetch", "next_update")
	}
	next, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("next_update %q does not parse as RFC 3339: %v", s, err)
	}
	if time.Now().After(next) {
		t.Errorf("next_update was %s, which is in the past\n"+
			"a verifier following §7.3 step 4 must re-fetch before relying on this manifest, so as "+
			"published it authenticates nothing", next.UTC().Format(time.RFC3339))
	}
}
