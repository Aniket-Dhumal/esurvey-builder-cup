package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every path the farmer app calls must be a route this server serves.
//
// # Why this test exists
//
// The app posted to /api/v1/auth/farmer/start for as long as it had a sign-in
// screen. The server has always served /api/v1/auth/farmer/otp. No farmer
// could sign in, and nothing caught it: the app's tests answer through a mock
// client that returns whatever the test wants whatever the path, and this
// package's tests never see the app. It surfaced the first time the release
// build ran on a handset, as a 404 in a red box.
//
// Two languages either side of an HTTP boundary is exactly where a contract
// goes unchecked, and "it compiles on both sides" says nothing about whether
// the strings match. So the check is a string comparison, because that is
// what the bug was.
//
// # Why it lives here
//
// The server owns the route table. Deleting or renaming a route should fail
// the tests of the package that did it, not of a project somebody may not
// build that week.
//
// It reads the Dart source rather than calling anything: no device, no
// emulator, no running server. If the app is not checked out beside the
// backend the test skips, so a backend-only clone is not made to fail by
// something it cannot see.
func TestTheFarmerAppOnlyCallsRoutesThatExist(t *testing.T) {
	appDir := filepath.Join("..", "..", "..", "mobile-app", "lib")
	if _, err := os.Stat(appDir); err != nil {
		t.Skipf("mobile-app is not checked out beside the backend: %v", err)
	}

	routes := registeredRoutes(t)
	if len(routes) == 0 {
		t.Fatal("no routes were parsed from server.go; the check would pass vacuously")
	}

	called := pathsCalledByTheApp(t, appDir)
	if len(called) == 0 {
		t.Fatal("no API paths were found in the app; the check would pass vacuously")
	}

	for _, p := range called {
		if !routes[p] {
			t.Errorf("the app calls %q and no route serves it.\n"+
				"A farmer meets this as a 404 with nothing they can do about it.\n"+
				"Registered routes:\n  %s",
				p, strings.Join(sortedKeys(routes), "\n  "))
		}
	}
}

var (
	// Paths only, for the check above. The verbs are compared separately, by
	// TestTheFarmerAppCallsThoseRoutesWithTheVerbTheyAnswer.
	//
	// It used to say here that the method was not worth comparing, because a path
	// under the wrong verb is a 405 and 405 is a different bug with its own
	// symptom. That was wrong, and it cost the live guidance feature an afternoon:
	// the app measured its uplink with PUT /api/v1/net/probe, which was registered
	// under POST alone, and the 405 was read as a failed measurement, then as an
	// uplink of zero, then as a farm too slow for a conversation. Nothing said 405
	// to anybody. The assistant was simply never offered, which is
	// indistinguishable from the condition it exists to detect.
	routePattern = regexp.MustCompile(
		`"(?:GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]*)"`)

	// The same table, as verb and path together.
	routeWithVerbPattern = regexp.MustCompile(
		`"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]*)"`)

	// A Dart call where the verb and the path are visible together.
	//
	// Deliberately narrow. It matches only where a quoted path follows the client
	// method directly -- across a line break, since that is how the formatter lays
	// out a call with a body -- and every other call is left to the path-only
	// check. A contract test that guesses is a contract test that cries wolf, and
	// this file has already learned twice what that costs.
	dartCallPattern = regexp.MustCompile(
		`api\.(get|postJson|postMultipart|post|putBytes|put|delete|patch)` +
			`\w*\(\s*'(/api/v1/(?:\$\{[^}]*\}|[^'])*)'`)

	// Single-quoted Dart string literals beginning /api/v1.
	// A Dart single-quoted string, where an interpolation may itself contain
	// quotes: '/api/v1/parcels/${Uri.encodeComponent('$farmId')}/stations'.
	//
	// Matching the path as [^']* ends it at the quote inside the interpolation
	// and yields "/api/v1/parcels/${Uri.encodeComponent(" — a path no route
	// serves, so the test failed and blamed the app for a route it calls
	// correctly. A false alarm in a contract test is worse than no test: the
	// next real break is read as the same known noise.
	dartPathPattern = regexp.MustCompile(`'(/api/v1/(?:\$\{[^}]*\}|[^'])*)'`)

	// Interpolation stands in for a path parameter.
	//
	// Both of Dart's forms. The braced one is obvious; the bare one is not,
	// and leaving it out made this test raise a false alarm the first time
	// somebody wrote '/api/v1/me/registry/$id' -- reporting a route as missing
	// when it was registered perfectly well. A check that cries wolf about
	// correct code is one people learn to skip, which costs far more than the
	// bug it was built to catch.
	//
	// Braced first, so ${a} is consumed whole rather than leaving a stray '{'.
	interpolation = regexp.MustCompile(`\$\{[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*`)

	// Go's ServeMux wildcards.
	wildcard = regexp.MustCompile(`\{[a-zA-Z]+\}`)
)

func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}

	out := map[string]bool{}
	for _, m := range routePattern.FindAllStringSubmatch(string(src), -1) {
		out[wildcard.ReplaceAllString(m[1], "{}")] = true
	}
	return out
}

func pathsCalledByTheApp(t *testing.T, dir string) []string {
	t.Helper()

	seen := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".dart") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range dartPathPattern.FindAllStringSubmatch(string(b), -1) {
			p := interpolation.ReplaceAllString(m[1], "{}")
			// A query string is the caller's business, not the route's.
			if i := strings.IndexByte(p, '?'); i >= 0 {
				p = p[:i]
			}
			seen[p] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the app: %v", err)
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dartVerbs maps the app's client methods to the verb they send.
//
// Named here rather than derived from the method name: postJson and
// postMultipart both send POST, putBytes sends PUT, and a rule that took the
// leading word would be a rule that broke the day somebody added postOrPut.
var dartVerbs = map[string]string{
	"get":           "GET",
	"post":          "POST",
	"postJson":      "POST",
	"postMultipart": "POST",
	"put":           "PUT",
	"putBytes":      "PUT",
	"delete":        "DELETE",
	"patch":         "PATCH",
}

// TestTheFarmerAppCallsThoseRoutesWithTheVerbTheyAnswer is the other half.
//
// # Why a 405 is not a lesser bug than a 404
//
// Because of what the app does with it. A 404 on a path the app needs tends to
// surface: something visible fails and somebody reports it. A 405 arrives inside
// a measurement or a retry, gets treated as the failure that measurement is built
// to tolerate, and the feature standing on it goes quiet.
//
// That is exactly what happened. PUT /api/v1/net/probe was registered under POST,
// the app read the 405 as an uplink of zero, and live guidance decided every farm
// in the state was too slow to carry a conversation. The walk worked, the uploads
// worked, no error reached a screen or a log, and the feature was simply absent.
//
// # Why only some calls are checked
//
// A path built up in pieces, or handed to a helper, has no verb visible at the
// call site, and this makes no attempt to chase one. It checks what it can see,
// and the path-only test covers the rest. Half a contract test that never lies is
// worth more than a whole one nobody trusts -- see the false alarms recorded
// above, both of which blamed correct code.
func TestTheFarmerAppCallsThoseRoutesWithTheVerbTheyAnswer(t *testing.T) {
	appDir := filepath.Join("..", "..", "..", "mobile-app", "lib")
	if _, err := os.Stat(appDir); err != nil {
		t.Skipf("mobile-app is not checked out beside the backend: %v", err)
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}

	// Which verbs each path answers, and which paths exist at all.
	answers := map[string]map[string]bool{}
	for _, m := range routeWithVerbPattern.FindAllStringSubmatch(string(src), -1) {
		path := wildcard.ReplaceAllString(m[2], "{}")
		if answers[path] == nil {
			answers[path] = map[string]bool{}
		}
		answers[path][m[1]] = true
	}
	if len(answers) == 0 {
		t.Fatal("no routes were parsed from server.go; the check would pass vacuously")
	}

	calls := appCallsWithVerbs(t, appDir)
	if len(calls) == 0 {
		t.Fatal("no verbed calls were found in the app; the check would pass vacuously")
	}

	for _, c := range calls {
		served, known := answers[c.path]
		if !known {
			// A path no route serves at all. Reported by the other test, with the
			// whole route table beside it; saying it twice would only teach people
			// that one of the two is noise.
			continue
		}
		if !served[c.verb] {
			t.Errorf("the app sends %s %s and that route answers only %s.\n"+
				"A 405 does not reach a farmer's screen: it is read as whatever "+
				"failure the calling code tolerates, and the feature above it "+
				"goes quiet.",
				c.verb, c.path, strings.Join(sortedKeys(served), ", "))
		}
	}
}

type appCall struct {
	verb string
	path string
}

func appCallsWithVerbs(t *testing.T, dir string) []appCall {
	t.Helper()

	seen := map[appCall]bool{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".dart") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range dartCallPattern.FindAllStringSubmatch(string(b), -1) {
			verb, ok := dartVerbs[m[1]]
			if !ok {
				continue
			}
			p := interpolation.ReplaceAllString(m[2], "{}")
			if i := strings.IndexByte(p, '?'); i >= 0 {
				p = p[:i]
			}
			seen[appCall{verb: verb, path: p}] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the app: %v", err)
	}

	out := make([]appCall, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].verb < out[j].verb
	})
	return out
}
