package auth

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A farmer grant: proof that one account has answered a knowledge check for
// one registry identity.
//
// # What problem this solves
//
// A mobile number and a registry identity are not the same thing. The OTP
// proves the number; it says nothing about which farmer records that number
// may read. The strict answer -- only the farmer whose registered mobile this
// is -- locks out every farmer whose AgriStack number is one they no longer
// hold, which in practice is a great many of them, and they are exactly the
// people who need the app most.
//
// So a farmer identifies the record another way (see the lookup handler), and
// what comes back is this: a short-lived, signed statement that says "this
// account answered for this farmer id, until this date". The handset keeps it
// beside the farmer in its own list and presents it to refresh that farmer's
// record.
//
// # Why a token and not a row
//
// The link between an account and the farmers it acts for is deliberately not
// kept on the server: it lives on the phone, which is where the farmer's own
// list of people lives. A signature carries the same fact without a table to
// keep in step, to leak, or to become the authoritative list of which
// households share a handset.
//
// The cost is that a grant cannot be withdrawn before it expires, which is
// why the lifetime is short enough to re-answer for and long enough not to
// nag: see GrantTTL.
//
// # What it is not
//
// Not an access token. It carries no roles, no jurisdiction and no session,
// and nothing authenticates with it -- a caller presents it *alongside* their
// access token to say which of their farmers they are asking about. It is
// signed by the same keyring so that rotation covers both, and it is a
// distinct `typ` so that neither can ever be presented as the other.

const typFarmerGrant = "farmer_grant"

// GrantTTL is how long a farmer stays unlocked on a handset before the
// knowledge check is asked again.
//
// Ninety days: a season. Long enough that a farmer is not re-answering in the
// middle of a claim, short enough that a handset sold, lost or passed on does
// not carry a working key to somebody's land records indefinitely.
const GrantTTL = 90 * 24 * time.Hour

// Grant errors, kept distinct from the access-token ones: a caller who
// presents an expired grant is asked to answer again, not to sign in again.
var (
	ErrGrantExpired   = errors.New("this farmer needs to be confirmed again")
	ErrGrantMalformed = errors.New("farmer grant is malformed")
	ErrGrantSignature = errors.New("farmer grant signature is not valid")
	ErrGrantClaims    = errors.New("farmer grant claims are not acceptable")
)

type grantClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Sub string `json:"sub"`
	Fid string `json:"fid"`
	Iat int64  `json:"iat"`
	Nbf int64  `json:"nbf"`
	Exp int64  `json:"exp"`
	Typ string `json:"typ"`
}

// IssueFarmerGrant records that userID has answered for farmerID.
func (k *Keyring) IssueFarmerGrant(userID, farmerID string, ttl time.Duration) (string, time.Time, error) {
	if userID == "" || farmerID == "" {
		return "", time.Time{}, fmt.Errorf("%w: a grant needs both an account and a farmer", ErrGrantClaims)
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)

	h, err := json.Marshal(header{Alg: algEdDSA, Typ: typJWT, Kid: k.signKID})
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(grantClaims{
		Iss: k.issuer, Aud: k.audience,
		Sub: userID, Fid: farmerID,
		Iat: now.Unix(), Nbf: now.Unix(), Exp: exp.Unix(),
		Typ: typFarmerGrant,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	signing := b64(h) + "." + b64(payload)
	sig := ed25519.Sign(k.signKey, []byte(signing))
	return signing + "." + b64(sig), exp, nil
}

// FarmerGranted returns the farmer id a grant carries, if it is valid and was
// issued to userID.
//
// The account is checked here rather than by the caller: a grant that is
// merely well-signed says nothing about who may use it, and a handset that
// copied another account's grant would otherwise present it successfully.
func (k *Keyring) FarmerGranted(token, userID string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrGrantMalformed
	}

	rawHeader, err := unb64(parts[0])
	if err != nil {
		return "", ErrGrantMalformed
	}
	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return "", ErrGrantMalformed
	}
	// One algorithm, checked before anything is verified. See token.go.
	if h.Alg != algEdDSA || h.Typ != typJWT {
		return "", ErrGrantSignature
	}
	pub, ok := k.verify[h.Kid]
	if !ok {
		return "", ErrUnknownKey
	}

	sig, err := unb64(parts[2])
	if err != nil {
		return "", ErrGrantMalformed
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return "", ErrGrantSignature
	}

	rawClaims, err := unb64(parts[1])
	if err != nil {
		return "", ErrGrantMalformed
	}
	var c grantClaims
	if err := json.Unmarshal(rawClaims, &c); err != nil {
		return "", ErrGrantMalformed
	}

	// A grant is not an access token and must never be usable as one, nor the
	// other way round. The type is checked before the claims are believed.
	if c.Typ != typFarmerGrant {
		return "", ErrGrantClaims
	}
	if c.Iss != k.issuer || c.Aud != k.audience {
		return "", ErrGrantClaims
	}
	if c.Sub == "" || c.Sub != userID {
		return "", ErrGrantClaims
	}
	if c.Fid == "" {
		return "", ErrGrantClaims
	}

	now := time.Now().UTC()
	if now.Add(clockSkew).Before(time.Unix(c.Nbf, 0)) {
		return "", ErrGrantClaims
	}
	if now.Add(-clockSkew).After(time.Unix(c.Exp, 0)) {
		return "", ErrGrantExpired
	}
	return c.Fid, nil
}
