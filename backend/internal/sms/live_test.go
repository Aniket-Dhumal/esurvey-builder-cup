package sms

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// One real message through the real gateway.
//
// Skipped unless ESURVEY_SMS_LIVE_TO names a handset, because the failure
// everybody fears from a test suite is the run that actually messages
// somebody. Set it, with the gateway's own settings in the environment, to
// prove the account, the header and the DLT template agree:
//
//	ESURVEY_SMS_LIVE_TO=9876543210 go test ./internal/sms/ -run Live -v
//
// It sends the one-time code message with a fixed code, so nothing about a
// real sign-in is involved.
func TestLiveGatewayDelivers(t *testing.T) {
	to := os.Getenv("ESURVEY_SMS_LIVE_TO")
	if to == "" {
		t.Skip("set ESURVEY_SMS_LIVE_TO to send one real message")
	}

	cfg := Config{
		Provider:  os.Getenv("ESURVEY_SMS_PROVIDER"),
		BaseURL:   os.Getenv("ESURVEY_SMS_URL"),
		Username:  os.Getenv("ESURVEY_SMS_USERNAME"),
		AuthKey:   os.Getenv("ESURVEY_SMS_AUTH_KEY"),
		SenderID:  os.Getenv("ESURVEY_SMS_SENDER_ID"),
		PEID:      os.Getenv("ESURVEY_SMS_PEID"),
		Templates: map[string]string{OTPEventCode: os.Getenv("ESURVEY_SMS_LIVE_TEMPLATE")},
		Timeout:   30 * time.Second,
	}
	if cfg.Templates[OTPEventCode] == "" {
		t.Fatal("set ESURVEY_SMS_LIVE_TEMPLATE to the approved DLT content id")
	}

	g, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	text := os.Getenv("ESURVEY_SMS_OTP_TEXT")
	if text == "" {
		text = DefaultOTPText
	}
	// A fixed, obviously-not-real code: this is a wire test, not a sign-in.
	body := strings.ReplaceAll(text, "{code}", "123456")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ref, err := g.Send(ctx, OTPEventCode, to, body)
	if err != nil {
		t.Fatalf("the gateway refused the message: %v", err)
	}
	t.Logf("accepted; provider reference %s", ref)
}
