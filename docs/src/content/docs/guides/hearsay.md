---
title: Hearsay
description: Use local and shared availability evidence safely.
---

Hearsay is an optional hint layer for debrid cache availability and Usenet completeness. Decypharr records outcomes learned from normal adds, imports, and repair work. It never schedules probes to create observations.

Local Hearsay, public network participation, and observation sharing are enabled by default. Advice remains in shadow mode, so shared evidence is measured but does not change download decisions until you explicitly select active mode.

## What is recorded

Decypharr records:

- whether a debrid add was served from cache;
- whether a repair found a torrent still available;
- whether a Usenet post completed successfully.

Subjects are stable content hashes, not titles, filenames, account credentials, or API keys. A published failure still reveals that the corresponding hash was attempted. Keep **Share observations** off if you do not want to disclose that signal.

## Advice modes

Hearsay starts in **Shadow** mode. Decypharr evaluates evidence and records coverage and verified accuracy, but preserves its normal download behavior.

Choose **Active** only after reviewing those metrics on the Stats page. In active mode, Decypharr can skip a debrid add when trusted evidence says it is not cached and uncached downloading is disabled. It can also reject a Usenet post when every configured backbone has strong, fresh incompleteness evidence. Unknown, weak, stale, or conflicting evidence never blocks normal work.

Local truth wins immediately. Remote evidence must meet all configured thresholds:

| Threshold | Default | Meaning |
|---|---:|---|
| `min_support` | `0.5` | Share of active source weight supporting the answer |
| `min_evidence` | `0.3` | Absolute supporting reputation weight |
| `min_sources` | `1` | Number of supporting sources |

Usenet rejection additionally requires at least two sources and evidence no older than 24 hours.

## Configuration

The Hearsay settings page exposes these options:

| Setting | JSON key | Default |
|---|---|---|
| Enable local Hearsay | `disabled` | enabled |
| Join the public network | `participate` | on |
| Share observations | `publish` | on |
| Advice mode | `advice_mode` | `shadow` |
| Sharing port | `port` | automatic |
| Discovery port | `gossip_port` | automatic |
| Update interval | `interval` | `30m` |
| Maximum relay storage | `max_storage_bytes` | 1 GiB |
| Maximum sources per namespace | `max_feeds_per_namespace` | `256` |
| Trusted publishers | `follow` | discover automatically |

Example:

```json
{
  "hearsay": {
    "participate": true,
    "publish": true,
    "advice_mode": "shadow",
    "min_support": 0.5,
    "min_evidence": 0.3,
    "min_sources": 1,
    "port": 0,
    "gossip_port": 0,
    "interval": "30m",
    "max_storage_bytes": 1073741824,
    "max_feeds_per_namespace": 256,
    "follow": []
  }
}
```

`publish` has no effect unless `participate` is also true. With `participate: true` and `publish: false`, Decypharr receives and relays evidence but keeps its observations local.

`follow` is an allowlist. When it is non-empty, Decypharr accepts only those publisher identities, disables open discovery for other identities, and removes retained feeds outside the list.

## Environment variables

Every setting can be supplied in Docker or another process environment:

```yaml
environment:
  - DECYPHARR_HEARSAY__PARTICIPATE=true
  - DECYPHARR_HEARSAY__PUBLISH=true
  - DECYPHARR_HEARSAY__ADVICE_MODE=shadow
  - DECYPHARR_HEARSAY__MIN_SUPPORT=0.5
  - DECYPHARR_HEARSAY__MIN_EVIDENCE=0.3
  - DECYPHARR_HEARSAY__MIN_SOURCES=1
  - DECYPHARR_HEARSAY__MAX_STORAGE_BYTES=1073741824
  - DECYPHARR_HEARSAY__MAX_FEEDS_PER_NAMESPACE=256
  - DECYPHARR_HEARSAY__FOLLOW=ed25519:a3f9...,ed25519:b101...
```

Fixed ports are optional. A publicly reachable relay can additionally set `DECYPHARR_HEARSAY__PORT` and `DECYPHARR_HEARSAY__GOSSIP_PORT`, then publish the matching TCP and UDP ports from its container.

The identity, observations, metrics, and retained generations live in the `hearsay` directory under Decypharr's config path.

## Upgrading from the older integration

The current integration uses Hearsay `v0.6.2` and the HSY2 protocol. HSY1 remote generations are incompatible and are discarded on startup; local observations and the long-term identity remain usable. Hearsay `v0.6.2` also waits for a valid generation pointer before advertising a feed.

The old `no_publish` setting is replaced by `publish`. Missing `participate` and `publish` values now default to `true`, matching the standalone daemon. Explicit `false` values remain respected. Set both to `false` for local-only operation, and move from shadow to active mode only after checking measured accuracy.

To turn Hearsay off completely, clear **Enable local Hearsay** or set `"disabled": true`.
