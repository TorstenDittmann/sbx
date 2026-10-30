# Network

Sandboxes start with no network. You opt in per server, per API key, or per sandbox, and the server keeps cloud metadata and private address ranges blocked either way.

## Modes

| Mode | Meaning |
|---|---|
| `none` | Loopback only. The default. |
| `full` | Public internet through the `sbx` bridge (`sbx0`, `172.30.0.0/16`): DNS, IPv4, IPv6 when the daemon has it, git, and package registries. |
| `allow` | Same bridge as `full`, but only to the hosts and CIDRs in a required allow list. Everything else is rejected. |

Set it on a sandbox:

```json
{
  "template": "python",
  "network": {
    "mode": "allow",
    "allow": ["pypi.org", "files.pythonhosted.org"],
    "deny": ["evil.example"],
    "egress_kbps": 10000,
    "max_connections": 128
  }
}
```

Allow and deny entries are hostnames, IP addresses, or CIDRs. Wildcards such as `*.example.com` are rejected, because the packet filter only sees addresses. List each hostname. Names are resolved when the sandbox is created and refreshed by the reaper.

Previews do not need a network. A server inside a `none` sandbox can still be reached through a [preview](/docs/previews/), because the server dials it over the agent.

## Inheritance

Three layers set the policy: the server (`network.*` in the [configuration](/docs/configuration/#network)), the [API key](/docs/api-keys/#network-policy-per-key), and the sandbox. Most specific first:

- **Mode**: the sandbox's, else the key's, else `network.default_mode`. A template's `network` (`none` or `full`) is used when the create request does not set one.
- **Ceiling**: a key's `mode` and `allow` are only defaults until the key has `max_mode` (`none`, `allow`, or `full`). Then a sandbox that asks for a looser mode gets `403`, a looser server default is narrowed to `max_mode`, and under `max_mode: allow` every sandbox allow entry must be one of the key's hostnames or inside one of its CIDRs.
- **Allow list**: the sandbox's if it sent `allow` (an empty array clears it), else the key's, else `network.egress_allow`.
- **Deny list**: the union of server, key, and sandbox. A sandbox cannot remove a deny.
- **`egress_kbps` and `max_connections`**: the tightest positive value wins. `0` means inherit, and all zeros means unlimited.

`allow` with an empty allow list is rejected. A warm pool sandbox is only handed to a key whose effective network matches the pool's exactly.

## What stays blocked

With the defaults, traffic from the bridge is rejected to:

- `169.254.0.0/16` (link-local and cloud metadata) and `100.100.100.200/32` (Alibaba metadata)
- RFC 1918 ranges: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- other non-public IPv4: carrier-grade NAT `100.64.0.0/10` (Tailscale and many internal networks), `0.0.0.0/8`, `127.0.0.0/8`, `192.0.0.0/24`, and `198.18.0.0/15`
- IPv6 link-local `fe80::/10`, unique-local `fc00::/7`, and `::1`

These rules run after a sandbox's own rules, so an allow list cannot punch through them. To change them:

- `network.private_allow`: CIDRs that skip the private-range reject, for example a VPN subnet. It cannot open metadata.
- `network.block_private: false`: turn the private-range reject off.
- `network.block_metadata: false`: the only way to reach metadata.

The reaper checks the baseline rules every 30 seconds and reinstalls them if a firewalld reload, a `dockerd` restart, or a flush removed them. If the firewall cannot be installed, `full` and `allow` creates fail and the container is removed.

## DNS and IPv6

`network.dns` is written into the sandbox's resolver config. The default is `1.1.1.1` and `8.8.8.8` with `timeout:2`, `attempts:3`, and `rotate`. IPv6 resolvers are left out of the default because they stall when the host has no IPv6 route.

`network.ipv6` is `auto` (follow the Docker daemon), `on`, or `off`. With IPv6 on, a newly created `sbx` bridge also gets `fd16:5b00::/64`. An existing bridge is left alone, and outbound IPv6 still needs a route on the host. Firecracker guests are IPv4 only.

## Bandwidth and connections

`egress_kbps` installs a token-bucket qdisc on the sandbox's host veth with `tc`, so the host needs `tc` and `nsenter`. `max_connections` is an iptables `connlimit`. Both fail closed: if the limit cannot be installed, the create fails.

Firecracker enforces allow, deny, and connection limits on its tap device, but not bandwidth. A positive `egress_kbps` on Firecracker is rejected.

## Installing packages at runtime

The default sandbox is uid 1000 with a read-only root filesystem. Tools that install into a directory you choose work on `/workspace` when the image already has them: `npm --prefix`, `pip --target`, and `GOPATH` or `GOCACHE` under `/workspace`.

`apk` and `apt` need to write the root filesystem. The operator can allow that server-wide:

```yaml
docker:
  read_only_rootfs: false
  sandbox_root: true
```

`sandbox_root` runs the container as uid 0 and adds only `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, and `SETUID`, so `apt` can drop to its own user. Every other capability stays dropped and `no-new-privileges` stays on.

Usually the better option is to install packages once with [build steps](/docs/templates/#build-steps) and start every sandbox from the cached image.

## Environment variables

`SBX_NETWORK_MODE`, `SBX_BLOCK_PRIVATE`, `SBX_BLOCK_METADATA`, `SBX_PRIVATE_ALLOW`, `SBX_DNS`, `SBX_IPV6`, `SBX_EGRESS_ALLOW`, `SBX_EGRESS_DENY`, `SBX_DEFAULT_EGRESS_KBPS`, and `SBX_DEFAULT_MAX_CONNECTIONS`. Lists are comma-separated. The full list is on the [configuration](/docs/configuration/#network) page.
