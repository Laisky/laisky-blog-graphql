# Search error evidence

Primary Go documentation was checked on 2026-10-13:

- [Error chains](https://pkg.go.dev/errors): `Is` and `As` inspect wrapped causes, including errors that unwrap to multiple causes. Search failover must retain those causes rather than stringify them. Public error text is a separate, safe diagnostic description.
- [HTTP client behavior](https://pkg.go.dev/net/http#Client.Do): non-success HTTP status does not itself cause `Client.Do` to return an error. Each provider must classify the received status explicitly before reading or decoding its body.

Classification uses typed errors, context sentinels and actual HTTP status. Error-message substrings, provider messages and arbitrary configured engine names are not evidence. An unknown error stays unknown. A DNS timeout remains DNS evidence; an untyped TLS handshake message stays unknown. An operation with mixed failure categories reports `manager/unknown`, with individual safe attempt logs for detail.
