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
| Maximum seeded torrents | `max_seeded_torrents` | `256` |
| Maximum sources per namespace | `max_feeds_per_namespace` | `256` |
| Trusted publishers | `follow` | discover automatically |
| Block private peers | `block_private_peers` | off |
| Blocked address ranges | `blocklist` | none |

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
    "max_seeded_torrents": 256,
    "max_feeds_per_namespace": 256,
    "follow": [],
    "block_private_peers": false,
    "blocklist": []
  }
}
```

`publish` has no effect unless `participate` is also true. With `participate: true` and `publish: false`, Decypharr receives and relays evidence but keeps its observations local.

`max_seeded_torrents` limits the number of retained swarms. This limit is separate from `max_storage_bytes`. Each swarm uses memory for peers and network state, even when its generation file is small. An omitted value or `0` uses the default of `256`. Negative values are invalid. Active transfers can temporarily exceed this limit. Restart Decypharr after you change this setting.

`block_private_peers` stops Decypharr from connecting to private addresses. This applies to swarm peers, DHT nodes, and discovery partners. The blocked ranges are RFC 1918, CGNAT (`100.64.0.0/10`), link-local, loopback, and IPv6 unique local addresses. Use this setting behind a VPN. There, DHT can return addresses from the VPN provider's internal network. These addresses are not reachable, and in a cluster they can collide with internal ranges. `blocklist` adds more ranges in CIDR format, for example `203.0.113.0/24`. Decypharr rejects a range that is not valid CIDR. Restart Decypharr after you change these settings.

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
  - DECYPHARR_HEARSAY__MAX_SEEDED_TORRENTS=256
  - DECYPHARR_HEARSAY__MAX_FEEDS_PER_NAMESPACE=256
  - DECYPHARR_HEARSAY__FOLLOW=ed25519:a3f9...,ed25519:b101...
  - DECYPHARR_HEARSAY__BLOCK_PRIVATE_PEERS=true
  - DECYPHARR_HEARSAY__BLOCKLIST=203.0.113.0/24,198.51.100.0/24
```

Fixed ports are optional. A publicly reachable relay can additionally set `DECYPHARR_HEARSAY__PORT` and `DECYPHARR_HEARSAY__GOSSIP_PORT`, then publish the matching TCP and UDP ports from its container.

The identity, observations, metrics, and retained generations live in the `hearsay` directory under Decypharr's config path.

## Upgrading from the older integration

The current integration uses Hearsay `v0.6.3` and the HSY2 protocol. It removes incompatible HSY1 remote generations at startup. It keeps local observations and the long-term identity. Hearsay waits for a valid generation pointer before it advertises a feed.

The old `no_publish` setting is replaced by `publish`. Missing `participate` and `publish` values now default to `true`, matching the standalone daemon. Explicit `false` values remain respected. Set both to `false` for local-only operation, and move from shadow to active mode only after checking measured accuracy.

To turn Hearsay off completely, clear **Enable local Hearsay** or set `"disabled": true`.
