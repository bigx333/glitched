package pack

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

type packageFile struct {
	Format    string            `json:"format"`
	Files     map[string]string `json:"files"`
	Signature Signature         `json:"signature"`
}

// ErrNotPackage says the bytes aren't a Glitched plugin package.
var ErrNotPackage = errors.New("that isn't a Glitched plugin package")

// Pack makes the signed plugin in folder a package, if it verifies.
func Pack(folder string) ([]byte, error) {
	b, err := readFolder(folder)
	if err != nil {
		return nil, err
	}
	if _, err := b.Manifest(); err != nil {
		return nil, err
	}
	p := packageFile{Format: PackageFormat, Files: map[string]string{}, Signature: b.Signature}
	for rel, data := range b.Files {
		p.Files[rel] = base64.StdEncoding.EncodeToString(data)
	}
	return json.Marshal(p)
}

// Unpack reads a package and verifies it: every path is plain, the sizes
// are within the limits, the signature covers exactly its files, and its
// plugin.json is one mygo-plugin reads. Nothing is written.
func Unpack(data []byte) (*Bundle, error) {
	if len(data) > MaxPackageBytes {
		return nil, fmt.Errorf("%w: the package is over %d MiB", ErrTooLarge, MaxPackageBytes>>20)
	}
	var p packageFile
	if err := json.Unmarshal(data, &p); err != nil || p.Format != PackageFormat || p.Files == nil {
		return nil, ErrNotPackage
	}
	if len(p.Files) > MaxFiles {
		return nil, fmt.Errorf("%w: it has more than %d files", ErrTooLarge, MaxFiles)
	}
	b := &Bundle{Files: make(map[string][]byte, len(p.Files)), Signature: p.Signature}
	total := 0
	for rel, enc := range p.Files {
		if !plainPath(rel) {
			return nil, fmt.Errorf("%w: %q", ErrBadPath, rel)
		}
		if base64.StdEncoding.DecodedLen(len(enc)) > MaxFileBytes+2 {
			return nil, fmt.Errorf("%w: %s is over %d MiB", ErrTooLarge, rel, MaxFileBytes>>20)
		}
		data, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("%w: %s isn't base64", ErrNotPackage, rel)
		}
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is over %d MiB", ErrTooLarge, rel, MaxFileBytes>>20)
		}
		if total += len(data); total > MaxBytes {
			return nil, fmt.Errorf("%w: it's over %d MiB", ErrTooLarge, MaxBytes>>20)
		}
		b.Files[rel] = data
	}
	if p.Signature.Format == "" {
		return nil, ErrUnsigned
	}
	if err := b.verify(); err != nil {
		return nil, err
	}
	if _, err := b.Manifest(); err != nil {
		return nil, err
	}
	return b, nil
}
