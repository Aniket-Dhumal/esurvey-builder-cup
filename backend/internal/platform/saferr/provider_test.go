package saferr

import "testing"

// The body Google returned on 14 September 2026 when an API key's IP
// restriction refused the call, as the reasoning service stored it, with the
// real IP and project number replaced by documentation values of the same
// shape.
const ipRestricted = `weather_specialist: 403 PERMISSION_DENIED. {'error': {'code': 403, ` +
	`'message': 'The provided API key has an IP address restriction. The ` +
	`originating IP address of the call (203.0.113.8) violates this restriction.', ` +
	`'status': 'PERMISSION_DENIED', 'details': [{'@type': ` +
	`'type.googleapis.com/google.rpc.ErrorInfo', 'reason': 'API_KEY_IP_ADDRESS_BLOCKED', ` +
	`'metadata': {'callerIp': '203.0.113.8', 'consumer': 'projects/100000000001'}}]}}`

func TestTheRealProviderErrorIsRecognised(t *testing.T) {
	if !LooksLikeProviderError(ipRestricted) {
		t.Fatal("the error that leaked was not recognised")
	}
	out, ok := Withheld(ipRestricted, "withheld")
	if !ok || out != "withheld" {
		t.Fatalf("Withheld = %q, %v", out, ok)
	}
}

func TestEachFingerprintIsRecognisedAlone(t *testing.T) {
	for _, s := range []string{
		"called from 203.0.113.8",
		"consumer projects/100000000001",
		"key AIzaSyA0123456789abcdefghijklmnopq",
		"generativelanguage.googleapis.com",
		"status PERMISSION_DENIED",
		`{"error": {"code": 500}}`,
		"Traceback (most recent call last):",
	} {
		if !LooksLikeProviderError(s) {
			t.Errorf("not recognised: %q", s)
		}
	}
}

// What the platform writes itself must pass untouched, or every assessment
// would be replaced with the withheld sentence.
func TestOurOwnSentencesAreLeftAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"timed out after 90s",
		"no ground photographs were supplied",
		"the model service refused this deployment's credential; an operator needs to check the key or its access restrictions",
		"NDVI peaked at 0.72 on 2026-08-10, a fall of 0.31 toward bare soil",
		"Leaf water near the incident was 0.25 NDMI, 0.07 below this field's normal.",
		"survey number 453, village LGD 562111",
	} {
		if LooksLikeProviderError(s) {
			t.Errorf("our own sentence was treated as a provider error: %q", s)
		}
	}
}
