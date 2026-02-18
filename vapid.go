package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
)

// GenerateVAPIDKeys generates a new ECDSA P-256 keypair and returns
// the public and private keys as base64url-encoded strings (no padding).
func GenerateVAPIDKeys() (publicKey, privateKey string, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ECDSA key: %w", err)
	}

	// Public key: uncompressed point (65 bytes: 0x04 || X || Y)
	pubBytes := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	publicKey = base64.RawURLEncoding.EncodeToString(pubBytes)

	// Private key: raw scalar D (32 bytes, zero-padded)
	privBytes := priv.D.FillBytes(make([]byte, 32))
	privateKey = base64.RawURLEncoding.EncodeToString(privBytes)

	return publicKey, privateKey, nil
}

// ParseVAPIDKeys decodes base64url-encoded VAPID keys and returns the
// parsed ECDSA private key (which includes the public key).
func ParseVAPIDKeys(publicKeyB64, privateKeyB64 string) (*ecdsa.PrivateKey, error) {
	pubBytes, err := base64.RawURLEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode VAPID public key: %w", err)
	}

	x, y := elliptic.Unmarshal(elliptic.P256(), pubBytes)
	if x == nil {
		return nil, fmt.Errorf("invalid VAPID public key (not a valid P-256 point)")
	}

	privBytes, err := base64.RawURLEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode VAPID private key: %w", err)
	}

	d := new(big.Int).SetBytes(privBytes)

	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     x,
			Y:     y,
		},
		D: d,
	}

	return priv, nil
}
