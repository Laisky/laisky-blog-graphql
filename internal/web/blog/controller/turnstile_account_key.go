package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	// accountKeyBytes is how much of the account HMAC is kept: 128 bits make a
	// collision between two tracked accounts negligible.
	accountKeyBytes = 16
	// maxAccountKeyInput bounds the account bytes folded and hashed per
	// request. The gate runs before the resolvers' length checks, and every
	// login store rejects accounts far shorter than this, so longer inputs only
	// collide with other invalid accounts.
	maxAccountKeyInput = 1024

	// accountSpellingDomain and accountIdentityDomain separate the two kinds
	// of account record that share one table.
	accountSpellingDomain = "spelling\x00"
	accountIdentityDomain = "identity\x00"
)

// looseCollators hands out Unicode collators that compare at primary strength
// (ignoring case, diacritics and width). A collate.Collator is not safe for
// concurrent use, hence the pool.
var looseCollators = sync.Pool{
	New: func() any { return collate.New(language.Und, collate.Loose) },
}

// foldAccount returns the key under which all spellings a login store may
// treat as one account fold together. It is the Unicode collation key at
// primary strength (CLDR root, case-, accent- and width-insensitive, with
// default-ignorable characters dropped), the comparison MySQL's
// utf8mb4_0900_ai_ci and similar collations apply to OneAPI usernames and
// emails. Folding more spellings together than a store does only makes a
// challenge appear sooner; folding fewer would split one account's budget.
// It returns nil for a blank account.
func foldAccount(account string) []byte {
	if len(account) > maxAccountKeyInput {
		account = account[:maxAccountKeyInput]
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return nil
	}

	collator, ok := looseCollators.Get().(*collate.Collator)
	if !ok {
		collator = collate.New(language.Und, collate.Loose)
	}
	defer looseCollators.Put(collator)

	var buf collate.Buffer
	key := collator.KeyFromString(&buf, account)
	if len(key) == 0 {
		// Only ignorable characters: still give the spelling a distinct key.
		return []byte(account)
	}
	return append([]byte(nil), key...)
}

// keyedHash returns the truncated HMAC-SHA256 of domain||value under the
// tracker's per-process key, hex encoded.
func (t *authChallengeTracker) keyedHash(domain string, value []byte) string {
	mac := hmac.New(sha256.New, t.accountHashKey)
	_, _ = mac.Write([]byte(domain)) // hash.Hash.Write never returns an error
	_, _ = mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil)[:accountKeyBytes])
}

// accountKey returns the keyed hash of the folded account spelling, or ""
// when the account is blank.
func (t *authChallengeTracker) accountKey(account string) string {
	folded := foldAccount(account)
	if folded == nil {
		return ""
	}
	return t.keyedHash(accountSpellingDomain, folded)
}

// identityKey returns the keyed hash of a store-resolved account identity
// (see model.User.RiskIdentity), or "" when identity is empty.
func (t *authChallengeTracker) identityKey(identity string) string {
	if identity == "" {
		return ""
	}
	return t.keyedHash(accountIdentityDomain, []byte(identity))
}
