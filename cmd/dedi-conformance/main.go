// Command dedi-conformance measures a DeDi implementation against the
// standard.
//
// It reads a manifest describing the implementation under test, runs the cases
// for every profile that manifest claims, and reports what passed. It never
// writes to the implementation and needs no credentials, so it is safe to
// point at a production node.
//
// Exit status is 0 when every claimed profile passed and 1 when any case
// failed, so CI can gate on it.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/theflywheel/dedi-conformance/manifest"
	"github.com/theflywheel/dedi-conformance/openapi"
	"github.com/theflywheel/dedi-conformance/suite"
)

// specPathDefault is where the standard lives in the published image. In a
// source checkout it is the spec submodule.
const specPathDefault = "spec/api/openapi.yaml"

func main() {
	var (
		manifestPath = flag.String("manifest", "dedi-conformance.json", "path to the manifest describing the implementation under test")
		specPath     = flag.String("spec", specPathDefault, "path to the DeDi OpenAPI document")
		profiles     = flag.String("profile", "", "comma-separated profiles to run (default: every profile the manifest claims)")
		format       = flag.String("format", "human", "report format: human, json or junit")
		out          = flag.String("out", "", "write the report to this file instead of stdout")
		timeout      = flag.Duration("timeout", 15*time.Second, "per-request timeout")
	)
	flag.Usage = usage
	flag.Parse()

	if err := run(*manifestPath, *specPath, *profiles, *format, *out, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "dedi-conformance: %v\n", err)
		os.Exit(2) // 2 = could not run; 1 is reserved for "ran, and failed"
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dedi-conformance — measure a DeDi implementation against the standard

  dedi-conformance --manifest ./dedi-conformance.json

Exit status: 0 conformant, 1 not conformant, 2 the suite could not run.

Profiles:
  core         the eight read endpoints of api/openapi.yaml
  versioning   version history, as_on time-travel, revocation visibility
  publication  the signed well-known manifest and the mandatory verification
               of publishing-dedi-files.md §7.3
  beckn        the Beckn_subscriber reference registry shape

Flags:
`)
	flag.PrintDefaults()
}

func run(manifestPath, specPath, profiles, format, out string, timeout time.Duration) error {
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}
	// --profile narrows what the manifest claims; it can never widen it, so a
	// run cannot report on a profile the operator did not claim.
	if profiles != "" {
		want := map[string]bool{}
		for _, p := range strings.Split(profiles, ",") {
			want[strings.TrimSpace(p)] = true
		}
		var kept []string
		for _, p := range m.Profiles {
			if want[p] {
				kept = append(kept, p)
			}
		}
		for p := range want {
			if !m.Claims(p) {
				return fmt.Errorf("--profile names %q, which the manifest does not claim", p)
			}
		}
		m.Profiles = kept
	}

	s, err := openapi.LoadSpec(specPath)
	if err != nil {
		return err
	}

	run := suite.New(m, s, timeout).Execute()

	w := os.Stdout
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return fmt.Errorf("creating %s: %w", out, err)
		}
		defer f.Close()
		w = f
	}
	switch format {
	case "human":
		run.WriteHuman(w)
	case "json":
		if err := run.WriteJSON(w); err != nil {
			return err
		}
	case "junit":
		if err := run.WriteJUnit(w); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown --format %q; want human, json or junit", format)
	}

	if !run.Passed() {
		os.Exit(1)
	}
	return nil
}
