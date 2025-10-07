# Security Policy

Status: hobby project; effectively unmaintained. Best‑effort only, no SLA.

Important: not internet‑facing. Do not expose any bot endpoint (WebSocket
client creds, HTTP API) directly to the public internet.

## Supported Versions

- No guaranteed support window. Use the latest commit or the most recent tag.
- The code targets Go `>= 1.25`. Older versions may work but aren't evaluated.

## Report a Vulnerability (preferred)

- Use GitHub's private advisory flow:
  https://github.com/deichbewohner/rocketbot/security/advisories/new
- Please avoid opening a public issue for new vulnerabilities.

If the advisory flow isn't available for you, open a minimal public issue
without exploit details and we'll move it private if possible.

## Scope (what this policy covers)

- This repository and its code only.

## Temporary Mitigations (pragmatic defaults)

- Keep the Go toolchain and deps updated; prefer latest minor/patch.
- Use HTTPS endpoints and rotate tokens regularly.
- Restrict ingress (bind `API_ADDR` to localhost or a private interface; place
  behind an authenticated proxy).
- Restrict egress with network policy/firewall: allow only the Rocket.Chat host
  and configured webhook endpoints (e.g., iptables/nftables, eBPF, or
  Kubernetes NetworkPolicy).
- Run the bot with least privilege and minimal environment.

## Triage & Response

- Best‑effort only. We may be slow to respond or unable to act.
- If you don't hear back in 30 days, assume unmaintained and disclose
  responsibly (no secrets, provide safe repro, and note mitigations).
