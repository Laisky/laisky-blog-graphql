# Verified crawler queue generation

Verified on 2026-10-11 against the official Redis command documentation:
[LPOP](https://redis.io/docs/latest/commands/lpop/) removes entries only from its
specified list key and [RPUSH](https://redis.io/docs/latest/commands/rpush/)
appends entries to its specified list key. Distinct pending keys therefore
separate already compiled consumers that use the old key from producers and
consumers using the current key. The retained disposable Redis regression also
exercises the actual shared Go producer/dequeue wrappers and JSON result codec.

This is protocol compatibility routing, not an authorization boundary against
an actor allowed to issue arbitrary Redis commands. No Redis ACL or host
network changes are required or claimed. The receipt verifier remains necessary
for every returned task regardless of the queue key. Operational procedure and
rollback requirements are in the [crawler contract](../manual/crawler_egress_policy.md).
