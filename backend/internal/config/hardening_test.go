package config

import (
	"strings"
	"testing"
)

// What a hardened environment refuses to start with.
//
// These are the settings that are convenient on a laptop and dangerous in a
// district office. Every one of them has a working default for development, so
// the only thing standing between "it worked on my machine" and a real
// deployment is this list -- which is why it is tested rather than trusted.

// hardened sets the minimum a staging or production environment needs, so each
// test below changes exactly one thing and the refusal cannot be coming from
// somewhere else.
func hardened(t *testing.T, overrides map[string]string) error {
	t.Helper()

	base := map[string]string{
		"ESURVEY_ENV":                      "production",
		"ESURVEY_JWT_SEED":                 strings.Repeat("A", 43) + "=",
		"ESURVEY_REDIS_PASSWORD":           "a-redis-password",
		"ESURVEY_OTP_SENDER":               "msg91",
		"ESURVEY_DATABASE_URL":             "postgres://u:p@db:5432/esurvey?sslmode=verify-full",
		"SATELLITE_API_KEY":                "a-satellite-key",
		"ESURVEY_CORS_ORIGINS":             "https://console.example.gov.in",
		"ESURVEY_BOOTSTRAP_ADMIN_USERNAME": "",
		"ESURVEY_BOOTSTRAP_ADMIN_PASSWORD": "",
		"ESURVEY_TRUSTED_PROXY_HOPS":       "1",
		"ESURVEY_AUDIT_CHAIN_SEED":         "an-audit-chain-seed",
		"ESURVEY_INTERNAL_TOKEN":           "an-internal-token",
		"ESURVEY_EVIDENCE_BUCKET":          "esurvey-evidence",
	}
	for k, v := range overrides {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}

	_, err := Load()
	return err
}

func TestALeftoverBootstrapPasswordRefusesToStart(t *testing.T) {
	// The finding.
	//
	// It is not a second way in: the account is only created when the user
	// table is empty. It is worse than that -- it is the live administrator's
	// password sitting in the environment, an orchestrator's secret store and
	// a shell history, months after whoever set it stopped thinking about it.
	//
	// Startup already warned. Nobody reads warnings from a service that
	// started successfully, which is exactly why this is now a refusal.
	err := hardened(t, map[string]string{
		"ESURVEY_BOOTSTRAP_ADMIN_PASSWORD": "the-password-that-made-the-admin",
	})
	if err == nil {
		t.Fatal("production started with the bootstrap password still set")
	}
	if !strings.Contains(err.Error(), "BOOTSTRAP_ADMIN_PASSWORD") {
		t.Errorf("the refusal does not name the variable to remove: %v", err)
	}
}

func TestDevelopmentIsNotHeldToThat(t *testing.T) {
	// A laptop needs the bootstrap credential to have a first administrator at
	// all. Applying the production rule here would mean nobody could start the
	// stack, and the rule would be deleted rather than obeyed.
	t.Setenv("ESURVEY_ENV", "development")
	t.Setenv("ESURVEY_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("ESURVEY_BOOTSTRAP_ADMIN_PASSWORD", "a-development-password")
	t.Setenv("SATELLITE_API_KEY", "k")
	t.Setenv("ESURVEY_DATABASE_URL", "postgres://u:p@localhost:5432/esurvey")

	if _, err := Load(); err != nil {
		t.Fatalf("development refused a bootstrap credential it needs: %v", err)
	}
}

func TestAPlaintextCORSOriginIsRefused(t *testing.T) {
	// Session credentials are attached to browser calls. An http:// origin
	// puts them on the wire in the clear.
	err := hardened(t, map[string]string{
		"ESURVEY_CORS_ORIGINS": "http://console.example.gov.in",
	})
	if err == nil {
		t.Fatal("production accepted a plaintext CORS origin")
	}
}

func TestOneTimeCodesToTheLogAreRefused(t *testing.T) {
	// The log sender prints codes to stdout, where anybody with log access can
	// sign in as any farmer.
	err := hardened(t, map[string]string{"ESURVEY_OTP_SENDER": "log"})
	if err == nil {
		t.Fatal("production accepted the log OTP sender")
	}
}

func TestAnUnauthenticatedRedisIsRefused(t *testing.T) {
	// Redis holds refresh tokens and OTP material. Unauthenticated, it is a
	// session store anyone on the network can read and write.
	err := hardened(t, map[string]string{"ESURVEY_REDIS_PASSWORD": ""})
	if err == nil {
		t.Fatal("production accepted an unauthenticated Redis")
	}
}

func TestAnEphemeralSigningKeyIsRefused(t *testing.T) {
	err := hardened(t, map[string]string{"ESURVEY_JWT_SEED": ""})
	if err == nil {
		t.Fatal("production accepted a generated signing key")
	}
}

func TestAFullyConfiguredProductionStarts(t *testing.T) {
	// The check that keeps the others honest. If the baseline itself failed to
	// load, every test above would pass for the wrong reason.
	if err := hardened(t, nil); err != nil {
		t.Fatalf("a correctly configured production environment was refused: %v", err)
	}
}

func TestAProductionWithNowhereToPutEvidenceIsRefused(t *testing.T) {
	// A season is terabytes of photographs and station clips. On a container
	// volume the symptom is a farmer's video refused at its last chunk, weeks
	// into an event, with the disk full (docs/PLATFORM_SCALE_REVIEW.md, A3).
	err := hardened(t, map[string]string{"ESURVEY_EVIDENCE_BUCKET": ""})
	if err == nil {
		t.Fatal("production started with no evidence bucket")
	}
	if !strings.Contains(err.Error(), "ESURVEY_EVIDENCE_BUCKET") {
		t.Fatalf("the refusal does not name the setting: %v", err)
	}
}

func TestPlaintextTransportIsRefused(t *testing.T) {
	// The largest residual risk this platform had, made into a refusal.
	//
	// Everything this service returns is a bearer token or a named farmer's
	// land, losses and photographs, and the networks it runs on are village
	// Wi-Fi and taluka offices. In the clear, a session belongs to whoever is
	// listening.
	//
	// Two configurations are acceptable: terminate TLS here, or sit behind a
	// proxy that does and say how far away it is. There is no third.
	err := hardened(t, map[string]string{
		"ESURVEY_TRUSTED_PROXY_HOPS": "0",
		"ESURVEY_TLS_CERT_FILE":      "",
		"ESURVEY_TLS_KEY_FILE":       "",
	})
	if err == nil {
		t.Fatal("production started with no transport encryption at all")
	}
	if !strings.Contains(err.Error(), "transport encryption") {
		t.Errorf("the refusal does not explain what is missing: %v", err)
	}
}

func TestTerminatingTLSHereIsAccepted(t *testing.T) {
	// The other acceptable configuration: no proxy, but a certificate.
	if err := hardened(t, map[string]string{
		"ESURVEY_TRUSTED_PROXY_HOPS": "0",
		"ESURVEY_TLS_CERT_FILE":      "/etc/esurvey/tls/fullchain.pem",
		"ESURVEY_TLS_KEY_FILE":       "/etc/esurvey/tls/privkey.pem",
	}); err != nil {
		t.Fatalf("a TLS-terminating deployment was refused: %v", err)
	}
}

func TestHalfConfiguredTLSIsRefused(t *testing.T) {
	// A certificate with no key starts a server that cannot serve. Better to
	// say so at startup than at the first request.
	err := hardened(t, map[string]string{
		"ESURVEY_TLS_CERT_FILE": "/etc/esurvey/tls/fullchain.pem",
	})
	if err == nil {
		t.Fatal("production accepted a certificate with no key")
	}
}

func TestAnUnkeyedAuditChainIsRefused(t *testing.T) {
	// Without a seed, somebody who can write rows can also recompute a
	// consistent chain over rows they invented -- and the people with database
	// credentials are the people the log exists to hold accountable.
	err := hardened(t, map[string]string{"ESURVEY_AUDIT_CHAIN_SEED": ""})
	if err == nil {
		t.Fatal("production accepted an unkeyed audit chain")
	}
}

func TestAPlaintextDatabaseConnectionIsRefused(t *testing.T) {
	// The other half of transport encryption, and for a while the forgotten
	// half: the API demanded TLS for its own traffic while every farmer
	// record and Aadhaar hash crossed to Postgres in the clear.
	for _, mode := range []string{"disable", "allow", "prefer"} {
		err := hardened(t, map[string]string{
			"ESURVEY_DATABASE_URL": "postgres://u:p@db:5432/esurvey?sslmode=" + mode,
		})
		if err == nil {
			t.Errorf("production accepted sslmode=%s", mode)
			continue
		}
		if !strings.Contains(err.Error(), "sslmode") {
			t.Errorf("sslmode=%s was refused for the wrong reason: %v", mode, err)
		}
	}
}

func TestAnUnspecifiedSslModeIsRefused(t *testing.T) {
	// libpq treats an absent sslmode as prefer, which falls back to plaintext
	// without saying so. An absent setting is not a neutral one.
	err := hardened(t, map[string]string{
		"ESURVEY_DATABASE_URL": "postgres://u:p@db:5432/esurvey",
	})
	if err == nil {
		t.Fatal("production accepted a database URL with no sslmode at all")
	}
}

func TestVerifyFullIsAccepted(t *testing.T) {
	if err := hardened(t, map[string]string{
		"ESURVEY_DATABASE_URL": "postgres://u:p@db:5432/esurvey?sslmode=verify-full",
	}); err != nil {
		t.Fatalf("a verified database connection was refused: %v", err)
	}
}

func TestRequireIsAcceptedAndNoted(t *testing.T) {
	// require encrypts but does not verify the server, so it stops passive
	// capture and not an active impostor. Allowed, because demanding a CA
	// nobody has configured gets worked around rather than met.
	if err := hardened(t, map[string]string{
		"ESURVEY_DATABASE_URL": "postgres://u:p@db:5432/esurvey?sslmode=require",
	}); err != nil {
		t.Fatalf("sslmode=require was refused: %v", err)
	}
}

func TestUnauthenticatedAnalysisServicesAreRefused(t *testing.T) {
	// The AI service holds the model credential and is handed photographs of a
	// named person's land; the SAR service runs billable Earth Engine work.
	// Neither checked its caller, on the reasoning that both are bound to
	// loopback — true of one deployment, and not a property of the services.
	err := hardened(t, map[string]string{
		"AI_SERVICE_URL":         "http://ai:8100",
		"ESURVEY_INTERNAL_TOKEN": "",
	})
	if err == nil {
		t.Fatal("production started with the analysis services unauthenticated")
	}
	if !strings.Contains(err.Error(), "INTERNAL_TOKEN") {
		t.Errorf("the refusal does not name the variable to set: %v", err)
	}
}

func TestATokenSatisfiesTheAnalysisServiceRule(t *testing.T) {
	if err := hardened(t, map[string]string{
		"AI_SERVICE_URL":         "http://ai:8100",
		"SAR_SERVICE_URL":        "http://sar:8200",
		"ESURVEY_INTERNAL_TOKEN": "a-shared-token",
	}); err != nil {
		t.Fatalf("a configured internal token was still refused: %v", err)
	}
}

func TestTheTokenIsAlwaysRequiredBecauseTheAgentAlwaysHasAUrl(t *testing.T) {
	// AI_SERVICE_URL carries a default, so clearing the variable does not
	// unconfigure the service -- it falls back to http://127.0.0.1:8100. The
	// agent is not optional in any real deployment, so in a hardened
	// environment the token is not optional either.
	//
	// Written down because the obvious reading of the rule above is "only when
	// a service is configured", and the first version of this test assumed an
	// empty variable would express "no agent". It does not.
	err := hardened(t, map[string]string{
		"AI_SERVICE_URL":         "",
		"SAR_SERVICE_URL":        "",
		"ESURVEY_INTERNAL_TOKEN": "",
	})
	if err == nil {
		t.Fatal("production started with no internal token; the agent still " +
			"had its default URL and was therefore still reachable and " +
			"unauthenticated")
	}
}
