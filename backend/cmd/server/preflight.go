package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/config"
)

// Checking that evidence can go to the bucket, before it does.
//
// # Why this is a command and not a runbook step
//
// Switching evidence from a local volume to Cloud Storage is two environment
// variables, and the failure modes are all silent-until-a-farmer: credentials
// the container cannot see, a bucket in the wrong project, a customer-managed
// key the service account may read but not use. Every one of them surfaces as
// a photograph that will not upload, at the moment somebody is standing in a
// field.
//
// So it is checked first, deliberately, by writing a real object through the
// same code path the platform uses and reading it back. A round trip that
// works is the only evidence worth having; everything short of it is an
// assumption.
//
//	docker compose run --rm --no-deps api evidence-preflight
//
// It writes one small object under a preflight/ prefix and deletes it. It
// touches nothing else.

func evidencePreflight() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration: %v\n", err)
		return 1
	}

	fmt.Println("Evidence preflight")
	fmt.Println(strings.Repeat("-", 60))

	if cfg.EvidenceBucket == "" {
		fmt.Println("ESURVEY_EVIDENCE_BUCKET is not set.")
		fmt.Println()
		fmt.Println("Evidence is written to the local volume, which is where it")
		fmt.Println("has been since the platform started. That is a working")
		fmt.Println("deployment, not a broken one -- but the volume is not")
		fmt.Println("encrypted with a customer-managed key, is not versioned, and")
		fmt.Println("does not survive the host.")
		fmt.Println()
		fmt.Println("To switch, set both of these and run this again:")
		fmt.Println("  ESURVEY_EVIDENCE_BUCKET=esurvey-evidence-501512")
		fmt.Println("  GOOGLE_ADC_FILE=$HOME/.config/gcloud/application_default_credentials.json")
		fmt.Println()
		fmt.Println("The credentials come from `gcloud auth application-default")
		fmt.Println("login` on the host. Deliberately not a service-account key: a")
		fmt.Println("downloaded key to this bucket would be a long-lived,")
		fmt.Println("silently copyable credential to every farmer photograph in")
		fmt.Println("the system.")
		return 2
	}

	fmt.Printf("bucket:      %s\n", cfg.EvidenceBucket)
	if cfg.EvidencePrefix != "" {
		fmt.Printf("prefix:      %s\n", cfg.EvidencePrefix)
	}

	adc := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	switch {
	case adc == "":
		fmt.Println("credentials: from the environment (metadata server or gcloud)")
	default:
		if st, err := os.Stat(adc); err != nil {
			fmt.Printf("credentials: %s -- NOT READABLE (%v)\n", adc, err)
			fmt.Println()
			fmt.Println("The container cannot see the credential file. On a")
			fmt.Println("developer machine this is almost always GOOGLE_ADC_FILE")
			fmt.Println("pointing somewhere the Docker daemon cannot reach, or")
			fmt.Println("`gcloud auth application-default login` not having been")
			fmt.Println("run on the host.")
			return 1
		} else if st.Size() == 0 {
			// The compose file defaults this mount to /dev/null so a
			// deployment without a bucket still starts. An empty file here
			// means the default is still in place.
			fmt.Printf("credentials: %s -- EMPTY\n", adc)
			fmt.Println()
			fmt.Println("The mount is in place but nothing is behind it, which is")
			fmt.Println("the /dev/null default. Set GOOGLE_ADC_FILE on the host.")
			return 1
		}
		fmt.Printf("credentials: %s\n", adc)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := blob.NewGCS(ctx, cfg.EvidenceBucket, cfg.EvidencePrefix)
	if err != nil {
		fmt.Printf("\nFAILED to open the bucket: %v\n", err)
		return 1
	}

	fmt.Println()
	fmt.Println("Writing a test object through the same path a photograph takes.")

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		return 1
	}
	key := "preflight/" + hex.EncodeToString(nonce[:]) + ".txt"
	payload := "esurvey evidence preflight " + time.Now().UTC().Format(time.RFC3339)

	if err := store.Put(ctx, key, "text/plain", []byte(payload)); err != nil {
		fmt.Printf("  write:  FAILED -- %v\n", err)
		explain(err)
		return 1
	}
	fmt.Println("  write:  ok")

	r, err := store.Get(ctx, key)
	if err != nil {
		fmt.Printf("  read:   FAILED -- %v\n", err)
		explain(err)
		return 1
	}
	got, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		fmt.Printf("  read:   FAILED -- %v\n", readErr)
		return 1
	}
	if string(got) != payload {
		fmt.Println("  read:   FAILED -- what came back is not what went in")
		return 1
	}
	fmt.Println("  read:   ok")

	if err := store.Delete(ctx, key); err != nil {
		// Not fatal: a bucket that forbids deletes is a legitimate retention
		// posture, and one stray preflight object is not a problem.
		fmt.Printf("  delete: could not remove the test object (%v)\n", err)
		fmt.Printf("          left at %s\n", key)
	} else {
		fmt.Println("  delete: ok")
	}

	fmt.Println()
	fmt.Println("Evidence can be written to the bucket and read back.")
	fmt.Println("Restart the API with ESURVEY_EVIDENCE_BUCKET set and every")
	fmt.Println("photograph from then on goes there. Anything already on the")
	fmt.Println("local volume stays where it is -- this switch is not a move.")
	return 0
}

// explain turns the usual failures into something actionable.
func explain(err error) {
	msg := strings.ToLower(err.Error())
	fmt.Println()
	switch {
	case strings.Contains(msg, "could not find default credentials"),
		strings.Contains(msg, "default credentials"):
		fmt.Println("  No credentials reached the client. Run this on the host:")
		fmt.Println("    gcloud auth application-default login")
		fmt.Println("  then set GOOGLE_ADC_FILE to the file it writes.")
	case strings.Contains(msg, "permission"), strings.Contains(msg, "403"):
		fmt.Println("  The identity is reaching the bucket and is not allowed to")
		fmt.Println("  write. It needs roles/storage.objectAdmin on the bucket,")
		fmt.Println("  and -- because the bucket has a customer-managed key --")
		fmt.Println("  roles/cloudkms.cryptoKeyEncrypterDecrypter on that key.")
		fmt.Println("  The second is the one that is usually missed: the error")
		fmt.Println("  reads as a storage problem and is a KMS one.")
	case strings.Contains(msg, "notfound"), strings.Contains(msg, "404"):
		fmt.Println("  The bucket does not exist, or exists in another project.")
	default:
		fmt.Println("  Not a failure this command recognises. The message above")
		fmt.Println("  is the client's own.")
	}
}
