// Package pack signs, packages, installs and updates Glitched plugins, so a
// plugin from someone else can be told apart from one changed after it was
// signed, and so the host runs only what was signed.
//
// A plugin is a folder: plugin.json, read by mygo-plugin, its module, and
// what else it ships. Signing it writes signature.json beside plugin.json:
//
//	{"format": "glitched-plugin-signature:v1",
//	 "key":    base64 ed25519 public key,
//	 "files":  {"plugin.json": sha256 hex, "index.js": sha256 hex, ...},
//	 "sig":    base64 ed25519 signature}
//
// The signature lives in its own file, not in plugin.json, so mygo-plugin
// reads manifests as it always did, and plugin.json is signed byte for byte
// as one of the files: nothing re-encodes it on the way.
//
// What is signed is every regular file under the folder but signature.json
// itself, any dot entry (.git, .DS_Store, .github, the .origin.json Install
// writes) and any node_modules folder. Those are what mygo-plugin's own Pack
// leaves out, and they are never loaded: the host loads a snapshot holding
// only the signed files, so leaving them out can't let unsigned code run. A
// plugin that needs code from node_modules bundles it. A link anywhere is
// refused, the folder itself included, since what it points to can change
// after signing without the signature noticing.
//
// The signature covers a text payload (see payload) rather than JSON, so
// anyone can rebuild it byte for byte without a canonical JSON encoder.
//
// A package (.glitched-plugin) is one JSON file:
//
//	{"format": "glitched-plugin:v1",
//	 "files": {"plugin.json": base64, "index.js": base64, ...},
//	 "signature": {the signature.json object}}
//
// JSON rather than zip: one decoder, and none of zip's duplicate entries,
// links, mode bits, central directory disagreeing with local headers or
// compression bombs to defend against. It costs base64's third, which
// plugins small enough to sign can afford. The manifest travels as the
// file plugin.json, never as a second copy that could disagree with it.
package pack

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// SignatureFormat names the signature's payload, so a signature over
	// anything else never verifies as a plugin's.
	SignatureFormat = "glitched-plugin-signature:v1"
	// PackageFormat is a package's format.
	PackageFormat = "glitched-plugin:v1"
	// Extension is a package file's extension, and what a GitHub release's
	// asset is picked by.
	Extension = ".glitched-plugin"

	SignatureFile = "signature.json"
	ManifestFile  = "plugin.json"
	// OriginFile is where an installed plugin came from, in its folder. A
	// dot file, so it's never part of what's signed.
	OriginFile = ".origin.json"
)

// The limits a signed plugin keeps, which bound what a package, a download
// or a clone can make Glitched read and write.
const (
	MaxFiles     = 2000
	MaxFileBytes = 8 << 20
	MaxBytes     = 32 << 20
	// MaxPackageBytes is its files' limit as base64, and room for the rest.
	MaxPackageBytes   = MaxBytes*4/3 + 1<<20
	maxSignatureBytes = 1 << 20
)

var (
	ErrUnsigned     = errors.New("the plugin isn't signed")
	ErrBadSignature = errors.New("the plugin's signature doesn't check out")
	ErrTooLarge     = errors.New("the plugin is too large")
	ErrLink         = errors.New("plugins can't contain links")
	ErrBadPath      = errors.New("the plugin has a file Glitched won't write")
)

// Tamper is how a file differs from what was signed.
type Tamper string

const (
	Missing Tamper = "missing"
	Extra   Tamper = "extra"
	Changed Tamper = "changed"
)

// TamperError says which file differs from what was signed, and how.
type TamperError struct {
	File string
	Kind Tamper
}

func (e *TamperError) Error() string {
	switch e.Kind {
	case Missing:
		return fmt.Sprintf("%s was signed but is missing", e.File)
	case Extra:
		return fmt.Sprintf("%s wasn't signed", e.File)
	}
	return fmt.Sprintf("%s changed since it was signed", e.File)
}

// ignored reports whether a folder entry named name is left out of what's
// signed (see the package doc).
func ignored(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// plainPath reports whether rel is a path Glitched writes inside a plugin's
// folder on every system: "/"-separated parts, none empty, "." or "..",
// none ignored, no backslash, colon or control character, and no Windows
// device name or trailing dot or space, each of which would land outside
// the folder or on another file there.
func plainPath(rel string) bool {
	if rel == "" || rel == SignatureFile || !utf8.ValidString(rel) || strings.ContainsAny(rel, `\:`) {
		return false
	}
	for _, r := range rel {
		if unicode.IsControl(r) {
			return false
		}
	}
	for part := range strings.SplitSeq(rel, "/") {
		if part == "" || part == ".." || ignored(part) || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || device(part) {
			return false
		}
	}
	return path.Clean(rel) == rel
}

func device(part string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(part), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9'
}
