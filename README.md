# dedi-conformance

A conformance suite for the [Decentralized Directory Protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol).
Point it at any DeDi implementation and it reports what that implementation
conforms to, and what it does not.

```sh
docker run --rm -v "$PWD:/work" flywheelai/dedi-conformance:v0.0.1 \
  --manifest /work/dedi-conformance.json
```

Published multi-arch (`linux/amd64`, `linux/arm64`) on Docker Hub, one image
per release. **Pin the version in anything you cite.** A conformance report is
a claim about an implementation, and it only means something if the suite that
produced it can be named exactly and fetched again unchanged — `latest` moves
with each release, so a report against it names a suite that no longer exists.

```
DeDi conformance — https://dedi.example.org

PASS   core           8 passed
PASS   versioning     5 passed
PASS   publication    7 passed
FAIL   beckn          1 passed, 1 failed
```

Exit status is 0 when every claimed profile passed, 1 when a case failed and 2
when the suite could not run — so it drops straight into CI. `--format junit`
or `--format json` for machine-readable output.

## What it does not do

**It never writes.** The standard specifies a read API and a file publication
model; it says nothing about how records get created. Every implementation has
its own write path, so a suite that seeded its own fixtures would be testing a
write API that is not in the standard, and would exclude conformant
implementations for not sharing ours.

It needs no credentials and issues only GETs, so it is safe to point at
production.

## The manifest

Because the suite does not create data, you tell it where your data already is.
It asks for *roles*, not names — "a record with more than one version" — and you
map each to something concrete in your deployment.

```json
{
  "base_url": "https://dedi.example.org",
  "publisher_domain": "example.org",
  "profiles": ["core", "versioning", "publication", "beckn"],
  "fixtures": {
    "namespace": "acme",
    "registry": "participants",
    "record": "bap.acme.example",
    "record_with_versions": "rotated.acme.example",
    "revoked_record": "gone.acme.example",
    "absent_namespace": "no-such-namespace-9f3a"
  }
}
```

`absent_namespace` must name something that does **not** exist. It is a fixture
rather than a string the suite invents because "obviously absent" is not the
suite's call — on your deployment our invented name might be real, and the case
would fail for the wrong reason.

Only the fixtures your claimed profiles need are required, and the manifest is
validated up front: you get the full list of what is missing, once, rather than
discovering them one run at a time.

## Profiles

Conformance is tiered, deliberately. A suite that demanded every capability
would be passed by exactly one implementation — whichever one it was written
against — and that is a specification of an implementation, not of a standard.
Claiming a profile is what opts its cases in.

| Profile | What it measures |
|---|---|
| **core** | The eight read endpoints of `api/openapi.yaml`: paths resolve, the `{message, data}` envelope, documented enum values are accepted, an absent namespace is 404 and not an empty success, errors carry a machine-readable code. |
| **versioning** | What the OpenAPI document declares parameters for but assigns no meaning to: version history, `as_on` returning the version live at that instant rather than the current one, and a revoked record remaining resolvable with `state: revoked`. |
| **publication** | `docs/publishing-dedi-files.md`: the signed manifest at the normative well-known path, and the verification its §7.3 makes mandatory — JCS canonicalization, detached JWS, `verification_method` resolving to a declared key, freshness. |
| **beckn** | The `Beckn_subscriber` reference registry shape, read from `schemas/Beckn_subscriber.json` at run time rather than transcribed. |

**publication is the profile that measures conformance most directly**, because
it is the part of the standard written in MUSTs. The OpenAPI document describes
an interface; §7.3 prescribes a verification a conformant party must perform, in
order, and states what it must refuse.

So that profile does not check for the *presence* of a `proof` block. It
performs the verification: canonicalizes the document with its proof removed,
and verifies the detached JWS against the key the manifest itself declares. A
publisher whose signatures do not verify fails, which is the entire point of
signing them.

A publisher claiming only `publication` needs no `base_url` — the standard says
an adopter need operate no infrastructure at all, and requiring a server would
contradict it.

### Deliberately out of scope

`subscribers.beckn.one`, the wildcard registry ONIX resolves against, appears
nowhere in the standard. Asserting it would make this suite a specification of
one ecosystem's deployment habits and would fail implementations that are
conformant. The beckn profile checks the record shape the standard *does* ship
and stops there.

## Why you can trust a green report

The characteristic failure of a conformance suite is not a false alarm. It is
passing everything, quietly, because an assertion stopped asserting — and
nothing about a green report reveals that.

So this repository contains a mock DeDi node that can be given specific defects,
one at a time, and the suite's own tests require **each defect to be caught by
the case meant to catch it**:

| Defect | Must be caught by |
|---|---|
| absent namespace answers 200 | *an absent namespace is 404* |
| no `{message, data}` envelope | *responses use the message/data envelope* |
| record omits resolvable fields | *a record lookup returns the fields a client resolves on* |
| 400s on a spec-listed enum value | *documented enum values are accepted* |
| 400s on a contract-valid `version_id` | *a version_id the contract permits is not a bad request* |
| revocation erases the record entirely | *a revoked record is still resolvable* |
| `as_on` accepted and ignored | *as_on returns the version live at that instant* |
| errors carry no `code` | *an error response carries a machine-readable code* |
| subscriber role outside the schema's enum | *a subscriber's type is one the reference schema permits* |
| subscriber missing a required field | *a subscriber record carries the reference schema's required fields* |

A defect caught only as collateral damage by some other case fails the test too.
Alongside these: a fully conformant mock must pass, no case may skip against it,
and a profile that ran zero cases must never report as passed.

Running the suite against a live node has already found bugs in the suite
itself — a version-history shape that the spec contradicts, and an `as_on`
assertion that compared the oldest version against the newest and so failed a
correct implementation for being correct. Both are fixed, and both are why the
mock alone is not enough.

## Embedding it in your own tests

If your implementation is in Go, you can skip the container entirely and run
the suite in-process against an `httptest.Server`. No deployment, no Docker
layer, and it runs on every `go test`:

```go
srv := httptest.NewServer(myHandler)          // your node
defer srv.Close()
seedFixtures(t)                               // your own write path

m := &manifest.Manifest{
    BaseURL:  srv.URL,
    Profiles: []string{manifest.ProfileCore, manifest.ProfileVersioning},
    Fixtures: map[string]string{ /* role -> your names */ },
}
specPath := "path/to/api/openapi.yaml"        // reference schemas resolve from ../schemas
spec, err := openapi.LoadSpec(specPath)
run := suite.New(m, spec, specPath, 10*time.Second).Execute()
if !run.Passed() { /* run.Results carries each failure and its request */ }
```

The packages are `manifest`, `openapi`, `suite` and `report`. Vendor this repo
as a git submodule and `replace` it in your `go.mod` to pin the exact suite
version your build is measured against.

## What the suite refuses to decide

Where the standard is genuinely silent and two designs are both defensible, the
case tests the property both must satisfy rather than picking a winner.

The clearest example is a revoked record. One design answers 200 with
`state: revoked` — self-describing, but only to a client that reads `state`,
and real Beckn clients do not: ONIX's `LookupNode` treats any 200 as a live
participant, so a revoked subscriber keeps being routed to. The other answers
404 on the live-binding read and keeps history reachable — every client honours
that, at the cost of a second call to learn why.

The standard fixes no status code here, so the case asserts only what both
designs must preserve: that a revoked record stays **distinguishable from one
that never existed**. Failing an implementation for the other choice would be
this suite inventing a requirement.

## Notes on the standard, found by building this

- **The OpenAPI document declares no `required` properties anywhere.** Taken
  literally, a record lookup could answer `{"message":"ok","data":{}}` and
  violate nothing. The core profile therefore asserts its own small set —
  `record_name`, `namespace`, `registry_name`, `state`, `version` — documented
  in `cases_core.go` as a judgment call, not a derivation. The JSON Schemas
  under `schemas/` *do* carry `required` arrays, and those are used as given.
- **`version_id` is `type: string`, unconstrained.** A non-numeric value is a
  well-formed request under the published contract, so rejecting it with 400
  tells a client its request was malformed when the spec says it was not. 404
  is the answer that fits.
- **The versions endpoint returns ids without timestamps.** "Which version was
  live at time T" cannot be answered from history alone, so the `as_on` case
  establishes each version's instant by looking that version up.

## Building from source

The spec is a git submodule pinned to a commit, so the suite always measures
against a fixed version of the standard rather than a moving `main`.

```sh
git clone --recurse-submodules https://github.com/theflywheel/dedi-conformance
cd dedi-conformance
go test ./...                                  # includes the anti-vacuity tests
go run ./cmd/dedi-conformance --manifest ./my-manifest.json
```

Moving the pin is a deliberate, reviewable step:

```sh
git -C spec fetch origin main
git -C spec log --oneline HEAD..origin/main    # read what changed
git -C spec checkout <new-sha>
go test ./... && git add spec && git commit
```

## Adding a case

1. Write it in the `cases_*.go` file for its profile. State what breaks for a
   real user when it fails — the failure text is what someone acts on.
2. Add a defect to `mock/` that the case catches, and an entry to
   `TestEachDefectIsCaught`. A case with no defect behind it is a case nobody
   can tell is working.
3. If it needs data, add a fixture role to `internal/manifest` and list it under
   the profile in `requiredByProfile`, so manifests are validated against it up
   front.

Cases must cite the standard, not our implementation. If the behaviour is not
in `spec/`, it does not belong here.

## Licence

Apache-2.0. The DeDi standard is Apache-2.0; `api/openapi.yaml` is MIT.
