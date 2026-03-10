// Package auth provides OAuth 2.0 Bearer-token middleware for the HTTP transport.
//
// Two validation modes are supported:
//   - "jwt"        – local validation using JWKS (no per-request network call after initial fetch)
//   - "introspect" – RFC 7662 token introspection (one network call per request, works with opaque tokens)
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config mirrors OAuthConfig from the main package so the auth package has no
// import cycle.
type Config struct {
	Enabled            bool
	Mode               string   // "jwt" or "introspect"
	Issuer             string
	JWKSUri            string
	Audience           string
	RequiredScopes     []string
	IntrospectEndpoint string
	ClientID           string
	ClientSecret       string
}

// Middleware wraps next with OAuth bearer-token validation when cfg.Enabled is
// true. When disabled it is a transparent pass-through.
func Middleware(cfg Config, next http.Handler) http.Handler {
	if !cfg.Enabled {
		return next
	}

	var v validator
	switch cfg.Mode {
	case "introspect":
		v = newIntrospectValidator(cfg)
	default: // "jwt" or unset
		v = newJWTValidator(cfg)
	}
	return &authHandler{validator: v, next: next}
}

type validator interface {
	validate(ctx context.Context, token string) error
}

type authHandler struct {
	validator validator
	next      http.Handler
}

func (h *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, err := extractBearerToken(r)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer realm="feedbackloop"`)
		http.Error(w, `{"error":"unauthorized","message":"`+sanitize(err.Error())+`"}`, http.StatusUnauthorized)
		return
	}

	if err := h.validator.validate(r.Context(), token); err != nil {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"forbidden","message":"`+sanitize(err.Error())+`"}`, http.StatusForbidden)
		return
	}

	h.next.ServeHTTP(w, r)
}

func extractBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fmt.Errorf("Authorization header must use Bearer scheme")
	}
	tok := strings.TrimPrefix(auth, "Bearer ")
	if tok == "" {
		return "", fmt.Errorf("empty Bearer token")
	}
	return tok, nil
}

// sanitize removes characters that would break an inline JSON string.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, `"`, `'`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// ──────────────────────────────────────────────────────────────────────────────
// JWT validator (JWKS-backed)
// ──────────────────────────────────────────────────────────────────────────────

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwksCache struct {
	mu        sync.RWMutex
	keys      map[string]jwkKey
	fetchedAt time.Time
	ttl       time.Duration
	uri       string
}

func newJWKSCache(uri string) *jwksCache {
	return &jwksCache{uri: uri, ttl: time.Hour, keys: map[string]jwkKey{}}
}

func (c *jwksCache) get(ctx context.Context, kid string) (jwkKey, error) {
	c.mu.RLock()
	stale := time.Since(c.fetchedAt) > c.ttl
	k, ok := c.keys[kid]
	c.mu.RUnlock()

	if ok && !stale {
		return k, nil
	}

	if err := c.refresh(ctx); err != nil && !ok {
		return jwkKey{}, fmt.Errorf("JWKS refresh failed: %w", err)
	}

	c.mu.RLock()
	k, ok = c.keys[kid]
	c.mu.RUnlock()

	if !ok {
		return jwkKey{}, fmt.Errorf("kid %q not found in JWKS", kid)
	}
	return k, nil
}

func (c *jwksCache) refresh(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.uri, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return err
	}

	var jwks struct {
		Keys []jwkKey `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("invalid JWKS response: %w", err)
	}

	c.mu.Lock()
	c.keys = make(map[string]jwkKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		c.keys[k.Kid] = k
	}
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

type jwtValidator struct {
	cfg   Config
	cache *jwksCache
}

func newJWTValidator(cfg Config) *jwtValidator {
	jwksURI := cfg.JWKSUri
	if jwksURI == "" && cfg.Issuer != "" {
		jwksURI = strings.TrimRight(cfg.Issuer, "/") + "/.well-known/jwks.json"
	}
	return &jwtValidator{cfg: cfg, cache: newJWKSCache(jwksURI)}
}

func (v *jwtValidator) validate(ctx context.Context, rawToken string) error {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("malformed JWT")
	}

	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid JWT header encoding")
	}
	payJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("invalid JWT payload encoding")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("invalid JWT signature encoding")
	}

	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return fmt.Errorf("invalid JWT header JSON")
	}

	jwk, err := v.cache.get(ctx, hdr.Kid)
	if err != nil {
		return err
	}

	sigInput := []byte(parts[0] + "." + parts[1])

	switch hdr.Alg {
	case "RS256", "RS384", "RS512":
		pub, err := rsaPublicKey(jwk)
		if err != nil {
			return err
		}
		if err := verifyRSA(hdr.Alg, sigInput, sigBytes, pub); err != nil {
			return fmt.Errorf("JWT signature invalid: %w", err)
		}
	case "ES256", "ES384", "ES512":
		pub, err := ecPublicKey(jwk)
		if err != nil {
			return err
		}
		if err := verifyECDSA(hdr.Alg, sigInput, sigBytes, pub); err != nil {
			return fmt.Errorf("JWT signature invalid: %w", err)
		}
	default:
		return fmt.Errorf("unsupported JWT algorithm: %s", hdr.Alg)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payJSON, &claims); err != nil {
		return fmt.Errorf("invalid JWT claims JSON")
	}
	return validateClaims(claims, v.cfg)
}

// ── Claims validation ─────────────────────────────────────────────────────────

func validateClaims(claims map[string]interface{}, cfg Config) error {
	now := time.Now().Unix()

	if exp, ok := claims["exp"].(float64); ok {
		if now > int64(exp) {
			return fmt.Errorf("token expired")
		}
	}
	if nbf, ok := claims["nbf"].(float64); ok {
		if now < int64(nbf) {
			return fmt.Errorf("token not yet valid")
		}
	}
	if cfg.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != cfg.Issuer {
			return fmt.Errorf("issuer mismatch: got %q, expected %q", iss, cfg.Issuer)
		}
	}
	if cfg.Audience != "" {
		if !audMatches(claims["aud"], cfg.Audience) {
			return fmt.Errorf("audience mismatch")
		}
	}
	if len(cfg.RequiredScopes) > 0 {
		scopeStr, _ := claims["scope"].(string)
		have := map[string]bool{}
		for _, s := range strings.Fields(scopeStr) {
			have[s] = true
		}
		for _, need := range cfg.RequiredScopes {
			if !have[need] {
				return fmt.Errorf("missing required scope: %s", need)
			}
		}
	}
	return nil
}

func audMatches(aud interface{}, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []interface{}:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// ── Crypto helpers ────────────────────────────────────────────────────────────

func rsaPublicKey(k jwkKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	return &rsa.PublicKey{N: n, E: e}, nil
}

func ecPublicKey(k jwkKey) (*ecdsa.PublicKey, error) {
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK y: %w", err)
	}
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported EC curve: %s", k.Crv)
	}
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}

func newHashForAlg(alg string) (hash.Hash, crypto.Hash, error) {
	switch alg {
	case "RS256", "ES256":
		return sha256.New(), crypto.SHA256, nil
	case "RS384", "ES384":
		return sha512.New384(), crypto.SHA384, nil
	case "RS512", "ES512":
		return sha512.New(), crypto.SHA512, nil
	}
	return nil, 0, fmt.Errorf("unknown algorithm: %s", alg)
}

func verifyRSA(alg string, sigInput, sig []byte, pub *rsa.PublicKey) error {
	h, ch, err := newHashForAlg(alg)
	if err != nil {
		return err
	}
	h.Write(sigInput)
	return rsa.VerifyPKCS1v15(pub, ch, h.Sum(nil), sig)
}

func verifyECDSA(alg string, sigInput, sig []byte, pub *ecdsa.PublicKey) error {
	h, _, err := newHashForAlg(alg)
	if err != nil {
		return err
	}
	h.Write(sigInput)
	digest := h.Sum(nil)

	// JWT ECDSA signatures are R || S concatenated (fixed-size, not DER).
	if len(sig)%2 != 0 || len(sig) == 0 {
		return fmt.Errorf("invalid ECDSA signature length")
	}
	half := len(sig) / 2
	r := new(big.Int).SetBytes(sig[:half])
	s := new(big.Int).SetBytes(sig[half:])

	if !ecdsa.Verify(pub, digest, r, s) {
		return fmt.Errorf("ECDSA signature verification failed")
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Token introspection validator (RFC 7662)
// ──────────────────────────────────────────────────────────────────────────────

type introspectValidator struct {
	cfg Config
}

func newIntrospectValidator(cfg Config) *introspectValidator {
	return &introspectValidator{cfg: cfg}
}

func (v *introspectValidator) validate(ctx context.Context, token string) error {
	if v.cfg.IntrospectEndpoint == "" {
		return fmt.Errorf("introspection endpoint not configured")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		v.cfg.IntrospectEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("introspection request error: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	if v.cfg.ClientID != "" {
		req.SetBasicAuth(v.cfg.ClientID, v.cfg.ClientSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("introspection request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("introspection endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("introspection response read error: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("invalid introspection response: %w", err)
	}

	if active, _ := result["active"].(bool); !active {
		return fmt.Errorf("token is not active")
	}

	return validateClaims(result, v.cfg)
}
