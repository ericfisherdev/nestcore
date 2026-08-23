package session

import (
	"encoding/gob"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// init registers, once per process, every concrete type either app puts into
// the session's map[string]interface{} — scs's GobCodec decodes that whole
// map in one gob.Decode call (see alexedwards/scs/v2's codec.go), so if
// EITHER app's process has not registered a type the OTHER app wrote under
// some key, decoding the ENTIRE shared session blob fails for every key, not
// just the unregistered one. Nestova puts a time.Time under
// mfa_verified_at and a webauthn.SessionData under a WebAuthn registration
// challenge key (NES-135/NES-136); Nestorage stores only plain strings today
// but must still be able to decode a session Nestova also wrote to over the
// shared identity.sessions store (NSTR-117) — registering both types here,
// in the one package both apps already import to build their manager,
// keeps that guarantee in one place instead of requiring Nestorage to import
// go-webauthn solely for this side effect.
//
// CONSEQUENCE FOR CHANGES: this list is a cross-repo contract, not a local
// detail. Putting a NEW concrete type into either app's session requires
// registering it HERE first, releasing nestcore, and having BOTH apps adopt
// that version BEFORE either one writes the type. Writing it from one app
// while the other still lacks the registration does not degrade gracefully
// — the other app's session Load fails for the whole blob, not just the new
// key. Neither app overrides scs's ErrorFunc, so LoadAndSave falls back to
// defaultErrorFunc: the gob error is logged and EVERY request carrying that
// cookie gets an HTTP 500. That is an outage for the lagging app's
// authenticated traffic, not a degraded login — the symptom to look for is a
// 500 storm with a gob type error in the logs.
func init() {
	gob.Register(time.Time{})
	gob.Register(webauthn.SessionData{})
}
