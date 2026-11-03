# Cloud Monitoring webhook references (2026-11-03)

Sources for `POST /webhook/gcp/alerts/cloud-monitoring`, checked 2026-11-03. Pages were read
through a summarizing fetch, so "not documented" below means nothing was found in
those reads, not that Google rules it out. For the release notes, only the 2025–2026
entries were read.

## Channels

[Notification options](https://docs.cloud.google.com/monitoring/support/notification-options)
lists Email, the Cloud console mobile app, PagerDuty, SMS, Slack, Webhooks, Pub/Sub
and Google Chat (Preview). Telegram is absent, and the
[release notes](https://docs.cloud.google.com/monitoring/docs/release-notes) add no
such channel. For an unsupported destination, Google suggests a pipeline through
Pub/Sub. A webhook endpoint must be public; Google directs private endpoints to
Pub/Sub.

## Webhook body

Monitoring posts the version 1.2 incident JSON, the same format Pub/Sub carries. No
custom body, template or header option is documented, so a GraphQL target needs a
translator, and this route is that translator. Only the policy's documentation block
is customizable, inside the JSON. See
[documentation variables](https://docs.cloud.google.com/monitoring/alerts/doc-variables).
Generated field formats "can change without notice", so the receiver reads only
structured fields and ignores everything else.

## Incident state and timestamps

[Notification options](https://docs.cloud.google.com/monitoring/support/notification-options)
(updated 2026-10-26) defines schema 1.2 `state` as "`open` or `closed`. If `open`,
then `ended_at` is null." `started_at` and `ended_at` are "number, time (in Unix
epoch seconds)". Google's examples are integers. Fractions and strings are not
documented.

The console also shows an **Acknowledged** alert state, an open alert marked by
hand ([incidents and events](https://docs.cloud.google.com/monitoring/alerts/incidents-events)).
Nothing documents it in the webhook `state`. The Alerts REST API, a separate
interface, uses `STATE_UNSPECIFIED`, `OPEN` and `CLOSED`, with no acknowledged value
([projects.alerts](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alerts)).
The receiver therefore maps only open and closed, compares them without regard to
case, and labels anything else `UNKNOWN-STATE` instead of guessing.

## Test Connection payload

Google says only that **Test Connection** sends "a test payload". Its contents are
undocumented. A capture published by
[Rootly](https://docs.rootly.com/integrations/google-cloud-monitoring), which notes
that it "differs from real alert payloads", is a populated incident, not an empty
one:

- `"version": "test"`, `"incident_id": "12345"` and `"state": "OPEN"`, in upper case.
- `"started_at": 0` and `"ended_at": 0`, which are zero rather than null.
- `"policy_name": "projects/12345/alertPolicies/12345"`, a resource path.
- `"summary": "Test Incident"` and `"url": "http://www.example.com"`.

The receiver keys off `version` to label it `TEST`, treats `0` as absent and does
not require `version` to be `1.2`. This is a third-party capture, so confirm it
with a real Test Connection during adoption.

## Authentication

[Channel configuration](https://docs.cloud.google.com/monitoring/alerts/using-channels-api)
offers two types, with no documented OIDC, bearer or custom-header option:

- `webhook_basicauth` takes `url`, `username` and `password`. The server must answer
  401 with a proper `WWW-Authenticate` header.
- `webhook_tokenauth` passes the token as a query parameter in the URL.

The route implements only Basic, which keeps the push token out of URLs.

## Delivery

No official page read gives a webhook retry count, backoff or timeout. The only
statement found is a
[June 2020 Google blog post](https://cloud.google.com/blog/products/management-tools/how-to-use-pubsub-as-a-cloud-monitoring-notification-channel):
a webhook "does retry deliveries a few times upon failure, if the target endpoint is
unavailable for too long, notifications are dropped entirely." Treat that as dated
and unquantified.

Per
[troubleshooting](https://docs.cloud.google.com/monitoring/alerts/troubleshoot-missing-notifications),
webhook failures appear only in Cloud Logging (`notification_channel_events`).
Webhooks, Slack, PagerDuty and the mobile app share one internal service, and Google
recommends email or Pub/Sub as the redundant channel.

For comparison, a [Pub/Sub subscription](https://docs.cloud.google.com/pubsub/docs/subscription-overview)
delivers at least once, redelivers unacknowledged messages and
[retains them](https://docs.cloud.google.com/pubsub/docs/subscription-properties) for
7 days by default. That durability starts only after Monitoring's publish succeeds,
and retries of the publish itself are not documented either.

Notification volume is documented: "a single notification channel can receive up
to 1,000 notifications at one time"
([concepts](https://docs.cloud.google.com/monitoring/alerts/concepts-indepth)).
Google publishes no source addresses for webhook deliveries, so an allowlist or a
per-sender budget at the gateway is not available.

## Timeouts

Google documents no webhook request timeout. Cloudflare's
[Error 524 page](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/)
(updated 2026-07-23) gives the default Proxy Read Timeout as **125 seconds**, up
from the long-quoted 100. The receiver's 20-second delivery deadline stays well
inside it.

## Abuse limits

The [OWASP Webhook Security Cheat Sheet](https://github.com/OWASP/CheatSheetSeries/blob/master/cheatsheets/Webhook_Security_Cheat_Sheet.md)
(updated 2026-10-03) says to rate limit "at the API gateway or application layer",
to answer excess with `429` and `Retry-After`, to return a generic `401`, and to
alert on spikes in authentication failures. The
[Authentication Cheat Sheet](https://github.com/OWASP/CheatSheetSeries/blob/master/cheatsheets/Authentication_Cheat_Sheet.md)
warns that lockout must not become a denial of service. With one shared credential
and no known sender addresses, the receiver therefore has no lockout. It caps only
failed lookups across all callers, and leaves per-address limits to the edge.

[Cloudflare rate-limiting rules](https://developers.cloudflare.com/waf/rate-limiting-rules/)
count per IP. The Free plan allows one rule, matched on path, over a 10-second
period. Business and higher can count only responses that match, as in Cloudflare's
[credential-stuffing example](https://developers.cloudflare.com/waf/rate-limiting-rules/best-practices/)
(`http.response.code in {401 403}`). That example uses a Managed Challenge, which a
webhook sender cannot pass, so use Block.
