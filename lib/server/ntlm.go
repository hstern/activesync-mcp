package server

import (
	"net/http"

	"github.com/Azure/go-ntlmssp"
)

// ntlmRoundTripper wraps base with the go-ntlmssp Negotiator, which
// handles the three-message NTLM handshake transparently per request.
//
// Username forms supported: "DOMAIN\user", "user@DOMAIN", and bare
// "user" (in which case go-ntlmssp negotiates without a domain).
func ntlmRoundTripper(base http.RoundTripper) http.RoundTripper {
	return ntlmssp.Negotiator{RoundTripper: base}
}
