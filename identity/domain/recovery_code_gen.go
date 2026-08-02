package domain

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// recoveryCodeAlphabet is Crockford's Base32 alphabet: it excludes the
// visually confusable 0/O, 1/I/L pairs so a code copied down by hand is
// unambiguous.
const recoveryCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// recoveryCodeEncoding is standard Base32 (RFC 4648, 5 bits per symbol)
// over the Crockford alphabet, unpadded. Using encoding/base32 (rather
// than mapping one alphabet symbol per input byte) is what makes
// recoveryCodeBytes' 80 bits of randomness actually become 16 output
// symbols (80 / 5 = 16, evenly divisible — 10 bytes is exactly two
// 5-byte Base32 groups, so NoPadding never needs to trim anything): a
// byte-per-symbol scheme would waste 3 of every 8 random bits per symbol
// and only produce one symbol per byte (10 symbols / 50 bits from the
// same 10 random bytes).
var recoveryCodeEncoding = base32.NewEncoding(recoveryCodeAlphabet).WithPadding(base32.NoPadding)

// recoveryCodeBytes is the raw random byte length per generated code. At
// 10 bytes (80 bits) this comfortably resists offline guessing even
// though each code is checked against a slow argon2id hash, matching the
// same KDF used for member passwords.
const recoveryCodeBytes = 10

// recoveryCodeGroupSize is how many Base32 symbols appear between the
// dashes GenerateRecoveryCode inserts for readability (e.g.
// "ABCD-EFGH-...").
const recoveryCodeGroupSize = 4

// GenerateRecoveryCode returns one raw, human-typable recovery code (16
// Crockford-Base32 characters — recoveryCodeBytes' full 80 bits, see
// recoveryCodeEncoding's doc — split into four dash-separated groups of
// four, e.g. "ABCD-EFGH-JKMN-PQRS"). The raw code is returned to the
// caller exactly once — only its hash is ever persisted.
func GenerateRecoveryCode() (string, error) {
	b := make([]byte, recoveryCodeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: generate recovery code: %w", err)
	}
	encoded := recoveryCodeEncoding.EncodeToString(b)

	var sb strings.Builder
	for i, r := range encoded {
		if i > 0 && i%recoveryCodeGroupSize == 0 {
			sb.WriteByte('-')
		}
		sb.WriteRune(r)
	}
	return sb.String(), nil
}

// NormalizeRecoveryCode uppercases and folds visually-confusable
// characters a member might type (O→0, I/L→1), then keeps only symbols
// from recoveryCodeAlphabet — an allow-list rather than stripping just
// the ASCII dash and space a member is expected to type: a code pasted
// from a password manager, a PDF, or a mobile keyboard's autocorrect can
// carry a tab, a non-breaking space, or an en/em dash instead, and a
// deny-list of only "-"/" " would leave those in place and silently fail
// to match the stored hash. ToUpper must run before the alias fold, so a
// lowercase o/i/l is still folded.
func NormalizeRecoveryCode(s string) string {
	s = recoveryCodeAliases.Replace(strings.ToUpper(s))
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(recoveryCodeAlphabet, r) {
			return r
		}
		return -1
	}, s)
}

// recoveryCodeAliases folds characters a member might type that are not
// in recoveryCodeAlphabet but are visually or phonetically equivalent to
// one that is.
var recoveryCodeAliases = strings.NewReplacer("O", "0", "I", "1", "L", "1")
