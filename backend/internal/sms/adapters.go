package sms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/notify"
)

// The two things in this platform that need to reach a person.
//
// One-time codes and notifications are the same delivery problem and
// different products: a code is a credential that must arrive in seconds and
// is worthless a minute later, and a notification is a courtesy that may
// queue for an hour. They are separated here so that a gateway outage during
// a district-wide send cannot delay somebody signing in, and so that the two
// carry their own DLT templates -- an operator registers an OTP template
// under a different category from a transactional one, and sending one under
// the other's header is how a header gets suspended.

// OTPEventCode is the template key one-time codes are registered under.
//
// A constant rather than a string at the call site: an OTP sent under a
// transactional template is a compliance failure that looks like a working
// system until an operator notices.
const OTPEventCode = "auth.otp"

// DefaultOTPText is the code message when a deployment names no other.
//
// Marathi, because the handset it reaches belongs to a farmer in Maharashtra.
// {code} is where the code goes.
const DefaultOTPText = "eSurvey: {code} हा तुमचा एक-वेळ कोड आहे. तो कोणालाही सांगू नका."

// OTP adapts a gateway to the authentication service's sender.
type OTP struct {
	Gateway *Gateway
	Log     *slog.Logger

	// Text is the message, with {code} where the code goes.
	//
	// Configurable because it is not ours to choose: the operator drops any
	// message whose text does not match the DLT template registered for the
	// header that sent it, and the aggregator answers 200 either way. A
	// deployment whose approved template reads differently sets this to that
	// template's exact wording. Empty means DefaultOTPText.
	Text string
}

func (o OTP) Name() string {
	if o.Gateway == nil {
		return "none"
	}
	return o.Gateway.Name()
}

// Send delivers a one-time code.
//
// The message text is built here rather than passed in, because it has to
// match the registered DLT template exactly and the authentication service
// has no reason to know what that says.
func (o OTP) Send(ctx context.Context, mobileE164, code string) error {
	if o.Gateway == nil {
		return ErrUnavailable
	}

	text := o.Text
	if strings.TrimSpace(text) == "" {
		text = DefaultOTPText
	}
	body := strings.ReplaceAll(text, "{code}", code)

	ref, err := o.Gateway.Send(ctx, OTPEventCode, mobileE164, body)
	if err != nil {
		// The code itself never reaches a log, whatever goes wrong: a
		// one-time code in a log file is a credential in a log file.
		o.Log.Warn("could not deliver a one-time code",
			"mobile", redact(digitsOf(mobileE164)), "error", err)
		return asSignInFailure(err)
	}
	o.Log.Info("one-time code sent",
		"mobile", redact(digitsOf(mobileE164)), "provider_ref", ref)
	return nil
}

// Notifications adapts a gateway to the outbox.
type Notifications struct {
	Gateway *Gateway
	Log     *slog.Logger
}

func (n Notifications) Name() string {
	if n.Gateway == nil {
		return "none"
	}
	return n.Gateway.Name()
}

// Send delivers one queued notification.
//
// The body was rendered when the message was queued, from a template that has
// to match the one registered with DLT. The event code selects that template,
// which is why it travels on the message rather than being inferred from the
// text.
func (n Notifications) Send(ctx context.Context, m notify.Message) (string, error) {
	if n.Gateway == nil {
		return "", ErrUnavailable
	}
	if m.Channel != "" && m.Channel != "sms" {
		// The outbox models four channels and only one is built. Refusing
		// clearly beats sending a WhatsApp message over SMS.
		return "", ErrRejected
	}
	return n.Gateway.Send(ctx, m.EventCode, m.Recipient, m.Body)
}

// digitsOf strips a number to digits for redaction, without validating it:
// a number that failed validation still has to be logged safely.
func digitsOf(s string) string {
	var b []rune
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b = append(b, r)
		}
	}
	return string(b)
}

// asSignInFailure says which of the two things went wrong, in the terms the
// authentication service promises a farmer.
//
// The distinction is the whole point: "the gateway is busy" is worth another
// press in a minute, and "no DLT template is registered" is not worth another
// press this season. Both look identical on the wire -- the aggregator answers
// 200 to several of them -- which is why the gateway separates them first.
func asSignInFailure(err error) error {
	switch {
	case errors.Is(err, ErrUnavailable):
		return fmt.Errorf("%w: %v", auth.ErrChannelDown, err)
	case errors.Is(err, ErrNoTemplate), errors.Is(err, ErrRejected),
		errors.Is(err, ErrBadNumber):
		return fmt.Errorf("%w: %v", auth.ErrUndeliverable, err)
	default:
		// An error from neither list is not classified as though it were:
		// guessing wrong here tells a farmer to keep trying something that
		// cannot work, or to give up on something that would have.
		return err
	}
}
