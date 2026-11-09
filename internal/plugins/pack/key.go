package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"glitched/internal/jsonfile"
)

// Key is this device's signing key. A plugin signed with it is the user's
// own, and turns on as soon as it's installed.
type Key struct {
	private ed25519.PrivateKey
}

// Public is the public key, as signature.json carries it.
func (k *Key) Public() string {
	return base64.StdEncoding.EncodeToString(k.private.Public().(ed25519.PublicKey))
}

// ID is KeyID of the public key.
func (k *Key) ID() string { return KeyID(k.Public()) }

func (k *Key) sign(message []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.private, message))
}

// KeyID is a short id of the public key pub (base64), stable across
// devices: the first 8 bytes of the raw key's sha256, as hex. It names a
// key to the user; trust is always decided on the whole key.
func KeyID(pub string) string {
	raw, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		raw = []byte(pub)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func keyPath(stateDir string) string { return filepath.Join(stateDir, "plugin-key") }

func trustedPath(stateDir string) string {
	return filepath.Join(stateDir, "plugin-trusted-keys.json")
}

// LoadKey reads this device's key from <state>/plugin-key, or makes and
// keeps one (0600) on first use. Two first uses at once, from two
// processes, agree on one key: each writes its own aside, and only the
// first to link it into place counts.
func LoadKey(stateDir string) (*Key, error) {
	if k, err := readKey(keyPath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		return k, err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(stateDir, "plugin-key.*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	err = pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if err := os.Link(f.Name(), keyPath(stateDir)); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return readKey(keyPath(stateDir))
}

func readKey(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: not a PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	private, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return &Key{private}, nil
}

// Keys are the keys whose plugins turn on at once: this device's own, and
// the keys the user trusts besides, such as their other devices'.
type Keys struct {
	Own     *Key
	Trusted []string
}

type trustedFile struct {
	Keys []string `json:"keys"`
}

// LoadKeys reads this device's key, making it on first use, and the keys
// trusted in <state>/plugin-trusted-keys.json.
func LoadKeys(stateDir string) (*Keys, error) {
	own, err := LoadKey(stateDir)
	if err != nil {
		return nil, err
	}
	var t trustedFile
	if err := jsonfile.Load(trustedPath(stateDir), &t); err != nil {
		return nil, err
	}
	return &Keys{Own: own, Trusted: t.Keys}, nil
}

// Trust adds the public key pub (base64) to the keys trusted under
// stateDir.
func Trust(stateDir, pub string) error {
	if b, err := base64.StdEncoding.DecodeString(pub); err != nil || len(b) != ed25519.PublicKeySize {
		return fmt.Errorf("%q is not an ed25519 public key", pub)
	}
	_, err := jsonfile.Edit(trustedPath(stateDir), func(t *trustedFile) error {
		if !slices.Contains(t.Keys, pub) {
			t.Keys = append(t.Keys, pub)
		}
		return nil
	})
	return err
}

// Trusts reports whether a plugin signed by pub turns on at once.
func (k *Keys) Trusts(pub string) bool {
	return k != nil && (k.Own != nil && pub == k.Own.Public() || slices.Contains(k.Trusted, pub))
}
