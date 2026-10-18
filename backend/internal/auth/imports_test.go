package auth

import (
	"os/exec"
	"strings"
	"testing"
)

// Packages this build must never depend on.
//
// # Why this is a test and not a comment
//
// golang.org/x/crypto/openpgp carries GO-2026-5932: unmaintained, unsafe by
// design, and no fix will ever be issued. It is in this module's graph because
// argon2 lives in the same module, and a module is the unit you can require --
// there is no way to take one package out of it.
//
// So the residual risk is not "we depend on something unfixable", it is
// "something unfixable is within reach". govulncheck reports it as not called
// today. This makes that a property of the build rather than an observation
// somebody made once, so the day a dependency starts importing it, a test
// fails instead of a scan report changing a line nobody reads.
func TestNothingReachesTheUnmaintainedCrypto(t *testing.T) {
	// The transitive import graph of the whole binary, as the compiler sees
	// it -- not a grep over source, which would miss anything reached through
	// a dependency.
	out, err := exec.Command("go", "list", "-deps", "../../cmd/server").Output()
	if err != nil {
		t.Skipf("go list is unavailable here: %v", err)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(deps) < 50 {
		t.Fatalf("only %d dependencies listed; the check would pass vacuously",
			len(deps))
	}

	present := make(map[string]bool, len(deps))
	for _, d := range deps {
		present[strings.TrimSpace(d)] = true
	}

	// The check discriminates: something the binary certainly does import.
	if !present["crypto/ed25519"] {
		t.Fatal("crypto/ed25519 is not in the dependency list, so this test is " +
			"not looking at the real import graph")
	}

	// Only openpgp is checked across the whole graph, because it is the only
	// one whose mere presence is the finding.
	//
	// An earlier version of this test also forbade crypto/md5, crypto/sha1 and
	// math/rand transitively, and failed immediately: TLS certificate handling
	// needs sha1 for legacy signatures, and protobuf uses math/rand. Their
	// presence in the graph is not a weakness -- using them *here* would be.
	// That is a different question, asked separately below.
	for _, pkg := range []string{"golang.org/x/crypto/openpgp"} {
		if present[pkg] {
			t.Errorf("the server binary now reaches %s. If a dependency pulled "+
				"it in, that dependency needs replacing; if this code did, it "+
				"needs undoing.", pkg)
		}
	}
}

// TestThisCodeUsesNoWeakPrimitives asks the question the graph cannot.
//
// crypto/md5, crypto/sha1 and math/rand are all legitimately present in the
// binary, reached through TLS and protobuf. What matters is whether anything
// in this repository reaches for them: every random value here keys a session,
// names a stored object or seeds a one-time code, and every hash here is
// either a credential or a piece of evidence's identity.
func TestThisCodeUsesNoWeakPrimitives(t *testing.T) {
	out, err := exec.Command("go", "list", "-f",
		"{{.ImportPath}} {{join .Imports \",\"}}",
		"github.com/agribid/esurvey-platform/backend/...").Output()
	if err != nil {
		t.Skipf("go list is unavailable here: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 10 {
		t.Fatalf("only %d packages listed; the check would pass vacuously",
			len(lines))
	}

	weak := map[string]string{
		"crypto/md5":   "collisions are practical; use crypto/sha256",
		"crypto/sha1":  "collisions are practical; use crypto/sha256",
		"math/rand":    "predictable; use crypto/rand for anything anyone could guess at",
		"math/rand/v2": "predictable; use crypto/rand for anything anyone could guess at",
	}

	var checked int
	for _, line := range lines {
		pkg, imports, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		checked++
		for _, imp := range strings.Split(imports, ",") {
			if why, bad := weak[strings.TrimSpace(imp)]; bad {
				t.Errorf("%s imports %s: %s", pkg, imp, why)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no packages were examined")
	}
	t.Logf("%d packages checked", checked)
}
