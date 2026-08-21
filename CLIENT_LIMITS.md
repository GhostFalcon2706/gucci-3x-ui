# Per-client speed, traffic multiplier and ISP lock

This branch adds three real, enforced per-client controls to the client editor
(Clients → Add / Edit → Basics).

## 1. Speed limit (`speedLevel`)

A bandwidth tier per client:

| Level | Ceiling      |
|-------|--------------|
| 0     | Unlimited    |
| 1     | 100 Mbps     |
| 2     | 50 Mbps      |
| 3     | 25 Mbps      |
| 4     | 10 Mbps      |
| 5     | 5 Mbps       |
| 6     | 2 Mbps       |
| 7     | 1 Mbps       |
| 8     | 0.5 Mbps     |

**How it is enforced.** Xray-core has no per-user rate limiter, so the cap is
applied by the Linux kernel: `internal/shaper` builds one HTB class per level on
the default-route interface and steers each online client IP (taken from the
panel's own IP tracker) into the class of its tier. `ClientShaperJob` reconciles
this every 10 seconds; clients on level 0 are never classified and keep the full
line rate.

**Requirements.** Linux, the `tc` binary (iproute2) and `CAP_NET_ADMIN`.
Managed container platforms (Railway, Fly, Heroku, …) do not grant
`CAP_NET_ADMIN`, so the shaper cannot run there — `shaper.Detect()` reports the
exact reason, the job stays idle, and the client editor shows a warning under
the field instead of pretending the limit is active. Run xray on a VPS (directly
or as a panel node) to get real shaping.

## 2. Traffic multiplier (`trafficMultiplier`)

How much quota each transferred byte consumes. `x2` means 20 MB of real traffic
is billed as 40 MB. Range 0.1 – 100, default 1.

Applied in `InboundService.addClientTraffic`: the delta reported by xray is
scaled per client before it is added to `client_traffics`. Inbound-level
counters are deliberately left unscaled so the panel's bandwidth statistics keep
showing reality.

The coefficient is also appended to the config name — `MyConfig (x2)` — in both
subscription output (`internal/sub`, `withMultiplierSuffix`) and the panel's own
links/QR codes (`clientMultiplierSuffix` in `frontend/src/lib/xray/inbound-link.ts`).

## 3. ISP / operator lock (`allowedIsps`)

Restricts a client's configs to selected access networks. The first option is
**All networks**, which means no restriction; selecting one or more operators
blocks everything else.

**How it is enforced.** For every distinct selection the panel:

1. resolves the announced IP prefixes of the chosen ISPs (`internal/isp`),
2. writes them into an xray geo asset, `bin/gucci-isp.dat`, under a stable code
   (`ISPLOCK…`), atomically and only after parsing the payload back,
3. prepends one routing rule per selection to the generated config:

```json
{
  "type": "field",
  "ruleTag": "gucci-isp-lock-ISPLOCK0B6A11F1C8",
  "user": ["client-email"],
  "source": ["ext:gucci-isp.dat:!ISPLOCK0B6A11F1C8"],
  "outboundTag": "gucci-isp-block"
}
```

The leading `!` inverts the match, so the rule fires exactly when the client
connects from **outside** its allowed networks and the connection is dropped by
an appended blackhole outbound. Clients without a lock are not referenced by any
rule, and the operator's own routing rules are preserved untouched behind the
injected ones.

**Data.** `internal/isp/data/prefixes.json` is a snapshot built from public
routing data: RIPE NCC's `asn.txt` (AS → organisation), ipverse/asn-ip and
RIPEstat (announced prefixes). Regenerate it with `tools/ispgen`-style tooling
when operators change address space.

**Known limits, by design:**

* MVNOs that announce no address space of their own (ArianTel, LotusTel, and
  operators such as ApTel/AzarTel that have no ASN at all) cannot be told apart
  from their host network. They are listed but marked unavailable, and a lock
  that would be unenforceable is skipped and reported instead of silently
  black-holing the client.
* WireGuard peers are not xray "users", so routing cannot match them by email —
  an ISP lock is not applied to WireGuard inbounds and a warning is logged.
* Satellite providers (Starlink, OneWeb, …) are not in the catalog yet; they can
  be added by dropping their ASNs into `internal/isp/catalog.go` and refreshing
  the prefix snapshot.

## Safety

Every injection fails **open** and logs the reason: if the asset cannot be
written or the routing section cannot be parsed, no rule is added and the rest
of the config is untouched. The generated config was validated with a real
xray-core binary (`xray -test -config`) — `Configuration OK.`

## API

`GET /panel/api/server/ispCatalog` returns the ISP catalog, the speed ladder,
the multiplier bounds, whether this host can shape traffic (and why not), and
the status of the last ISP-lock injection.
