# Crawler failure classification references

Verified 2026-10-14 against primary documentation; synthetic regression tests, rather than these references, establish the source behavior.

- [Go context](https://pkg.go.dev/context): cancellation of a derived context propagates to its children. `Cause` can distinguish the recorded cancellation cause from the generic `Err`; worker-local derived cancellation is not evidence that a remote caller canceled. Renderer cleanup and the primary operation failure require separate consideration.
- [Go errors](https://pkg.go.dev/errors): `Is` and `As` traverse wrapped error trees, including joined children. A joined cleanup cancellation can therefore satisfy `Is(Canceled)` even when the primary error has another cause. Retaining the tree preserves identity; diagnostic classification must use the correct operation scope and primary failure.
- [Cloudflare interstitial challenge pages](https://developers.cloudflare.com/cloudflare-challenges/challenge-types/challenge-pages/): challenge pages interrupt navigation with a full-page interstitial. A phrase appearing in otherwise ordinary page content does not by itself establish that such an interstitial blocked navigation.
- [Cloudflare Turnstile embedding](https://developers.cloudflare.com/turnstile/get-started/client-side-rendering/): a widget can be embedded in an ordinary form or page. Widget markup or a hidden response field alone is not evidence of a full-page challenge.

The bounded detector is a content classifier, not an authentication bypass or network security boundary. Egress admission, pinned transport, response budgets, launcher enforcement and receipt verification remain unchanged. No claim is made that the local classifier detects every future challenge layout or reconstructs the live incident.
