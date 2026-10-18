package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Access tokens are JWTs signed with Ed25519, and this file is the whole
// implementation.
//
// # Why not a JWT library
//
// Every serious JWT vulnerability of the last decade came from algorithm
// agility: alg=none accepted as valid, an RSA public key replayed as an HMAC
// secret, a signature verified with the algorithm the attacker named in the
// header. A library gives you those algorithms and then a configuration
// option to switch them off.
//
// This implementation has one algorithm. The header is checked for exactly
// "EdDSA" before anything else happens, there is no code path that verifies
// with anything else, and the parser refuses tokens it does not recognise
// rather than trying to be liberal. That is a smaller and more auditable
// surface than a general-purpose library configured down to the same thing.
//
// # Why asymmetric
//
// The assessment agent, and later the mobile BFF, need to verify tokens. With
// Ed25519 they hold a public key and can check a caller without holding
// anything that would let them mint one.

const (
	algEdDSA  = "EdDSA"
	typJWT    = "JWT"
	typAccess = "access"

	// clockSkew tolerates disagreement between the signer's clock and the
	// verifier's. Small: these run in the same cluster, and a generous window
	// extends the life of a revoked token.
	clockSkew = 30 * time.Second
)

// Token errors. Callers distinguish expiry, which is ordinary and answered by
// a refresh, from the rest, which is not.
var (
	ErrTokenExpired   = errors.New("access token has expired")
	ErrTokenMalformed = errors.New("access token is malformed")
	ErrTokenSignature = errors.New("access token signature is not valid")
	ErrTokenClaims    = errors.New("access token claims are not acceptable")
	ErrUnknownKey     = errors.New("access token was signed by an unknown key")
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// grantClaim is a role grant, abbreviated because it is repeated per grant in
// a token that travels on every request.
type grantClaim struct {
	Role  string `json:"r"`
	JurID string `json:"ji,omitempty"`
	JurCd string `json:"jc,omitempty"`
	JurLv string `json:"jl,omitempty"`
	JurNm string `json:"jn,omitempty"`
}

type claims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Sub string `json:"sub"`
	Sid string `json:"sid"`
	Jti string `json:"jti"`
	Iat int64  `json:"iat"`
	Nbf int64  `json:"nbf"`
	Exp int64  `json:"exp"`

	Typ  string       `json:"typ"`
	Kind string       `json:"knd"`
	Name string       `json:"nam,omitempty"`
	Fid  string       `json:"fid,omitempty"`
	Grt  []grantClaim `json:"grt,omitempty"`
	Mcp  bool         `json:"mcp,omitempty"`
}

// Keyring holds the signing key and any keys still trusted for verification.
//
// Rotation without signing anyone out: deploy the new seed as the signer and
// keep the old one in the verify set for one access-token lifetime, then drop
// it.
type Keyring struct {
	signKID  string
	signKey  ed25519.PrivateKey
	verify   map[string]ed25519.PublicKey
	issuer   string
	audience string
}

// NewKeyring builds a keyring from base64 (standard, padded or raw) 32-byte
// Ed25519 seeds. The first is the signing key; the rest verify only.
func NewKeyring(issuer, audience, signSeed string, previousSeeds []string) (*Keyring, error) {
	priv, err := parseSeed(signSeed)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}

	kr := &Keyring{
		signKey:  priv,
		verify:   map[string]ed25519.PublicKey{},
		issuer:   issuer,
		audience: audience,
	}

	pub := priv.Public().(ed25519.PublicKey)
	kr.signKID = keyID(pub)
	kr.verify[kr.signKID] = pub

	for i, s := range previousSeeds {
		p, err := parseSeed(s)
		if err != nil {
			return nil, fmt.Errorf("previous key %d: %w", i+1, err)
		}
		ppub := p.Public().(ed25519.PublicKey)
		kr.verify[keyID(ppub)] = ppub
	}

	return kr, nil
}

// GenerateSeed returns a fresh base64-encoded Ed25519 seed, for provisioning.
func GenerateSeed() (string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(seed), nil
}

// SigningKeyID identifies the current signing key, for logs and for the
// public key document.
func (k *Keyring) SigningKeyID() string { return k.signKID }

// PublicKeys returns every key a verifier should trust, keyed by kid.
func (k *Keyring) PublicKeys() map[string]string {
	out := make(map[string]string, len(k.verify))
	for kid, pub := range k.verify {
		out[kid] = base64.StdEncoding.EncodeToString(pub)
	}
	return out
}

// Issue mints an access token for a principal.
func (k *Keyring) Issue(p *rbac.Principal, ttl time.Duration) (string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)

	jti, err := randomID()
	if err != nil {
		return "", time.Time{}, err
	}

	c := claims{
		Iss:  k.issuer,
		Aud:  k.audience,
		Sub:  p.UserID,
		Sid:  p.SessionID,
		Jti:  jti,
		Iat:  now.Unix(),
		Nbf:  now.Unix(),
		Exp:  exp.Unix(),
		Typ:  typAccess,
		Kind: string(p.Kind),
		Name: p.Name,
		Fid:  p.FarmerID,
		Mcp:  p.MustChangePassword,
	}
	for _, g := range p.Grants {
		c.Grt = append(c.Grt, grantClaim{
			Role:  string(g.Role),
			JurID: g.JurisdictionID,
			JurCd: g.JurisdictionCode,
			JurLv: string(g.JurisdictionLevel),
			JurNm: g.JurisdictionName,
		})
	}

	h, err := json.Marshal(header{Alg: algEdDSA, Typ: typJWT, Kid: k.signKID})
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", time.Time{}, err
	}

	signing := b64(h) + "." + b64(payload)
	sig := ed25519.Sign(k.signKey, []byte(signing))

	return signing + "." + b64(sig), exp, nil
}

// Parse verifies a token and returns the principal it carries.
func (k *Keyring) Parse(token string) (*rbac.Principal, error) {
	// Exactly three segments. Splitting without a limit and checking the
	// count is what stops a fourth segment being ignored.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrTokenMalformed
	}

	rawHeader, err := unb64(parts[0])
	if err != nil {
		return nil, ErrTokenMalformed
	}
	var h header
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return nil, ErrTokenMalformed
	}

	// Before any parsing of claims and before any signature work: this
	// implementation speaks one algorithm, and a token claiming another is
	// rejected rather than negotiated.
	if h.Alg != algEdDSA || h.Typ != typJWT {
		return nil, fmt.Errorf("%w: alg=%q typ=%q", ErrTokenSignature, h.Alg, h.Typ)
	}

	pub, ok := k.verify[h.Kid]
	if !ok {
		return nil, ErrUnknownKey
	}

	sig, err := unb64(parts[2])
	if err != nil {
		return nil, ErrTokenMalformed
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, ErrTokenSignature
	}

	// Only now is the payload worth reading.
	rawClaims, err := unb64(parts[1])
	if err != nil {
		return nil, ErrTokenMalformed
	}
	var c claims
	if err := json.Unmarshal(rawClaims, &c); err != nil {
		return nil, ErrTokenMalformed
	}

	now := time.Now().UTC()
	switch {
	case c.Typ != typAccess:
		// A refresh token replayed at an API endpoint lands here.
		return nil, fmt.Errorf("%w: token type %q", ErrTokenClaims, c.Typ)
	case c.Iss != k.issuer:
		return nil, fmt.Errorf("%w: issuer %q", ErrTokenClaims, c.Iss)
	case c.Aud != k.audience:
		return nil, fmt.Errorf("%w: audience %q", ErrTokenClaims, c.Aud)
	case c.Sub == "":
		return nil, fmt.Errorf("%w: no subject", ErrTokenClaims)
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(clockSkew)):
		return nil, ErrTokenExpired
	case c.Nbf != 0 && now.Before(time.Unix(c.Nbf, 0).Add(-clockSkew)):
		return nil, fmt.Errorf("%w: not yet valid", ErrTokenClaims)
	case c.Iat != 0 && now.Before(time.Unix(c.Iat, 0).Add(-clockSkew)):
		return nil, fmt.Errorf("%w: issued in the future", ErrTokenClaims)
	}

	p := &rbac.Principal{
		UserID:             c.Sub,
		Kind:               rbac.Kind(c.Kind),
		Name:               c.Name,
		SessionID:          c.Sid,
		FarmerID:           c.Fid,
		MustChangePassword: c.Mcp,
	}
	for _, g := range c.Grt {
		role := rbac.Role(g.Role)
		if !role.Valid() {
			// A role this build does not know is dropped rather than
			// carried: an unknown role must not become an unchecked one.
			continue
		}
		p.Grants = append(p.Grants, rbac.Grant{
			Role:              role,
			JurisdictionID:    g.JurID,
			JurisdictionCode:  g.JurCd,
			JurisdictionLevel: rbac.Level(g.JurLv),
			JurisdictionName:  g.JurNm,
		})
	}

	return p, nil
}

func parseSeed(encoded string) (ed25519.PrivateKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("seed is empty")
	}

	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		seed, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("seed is not valid base64")
		}
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed is %d bytes, need %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// keyID is a short, stable fingerprint of a public key.
func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
