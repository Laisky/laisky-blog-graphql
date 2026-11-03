# Cloud Monitoring webhook references (2026-11-03)

Sources for `POST /alerts/cloud-monitoring`, checked 2026-11-03. Pages were read
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
