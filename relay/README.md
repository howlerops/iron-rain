# relay/ — stateless ciphertext forwarder

Lets the app reach the daemon from anywhere without opening inbound ports. Both the daemon and the app
dial the relay **outbound**; the relay bridges them by `serverID`. It only ever sees **ciphertext** +
routing IDs — it cannot read session traffic (E2EE terminates in the app and daemon).

- **Hosted** (default): configured by the installer.
- **Self-host**: run your own; point the daemon at it via config. (Impl + deploy docs land here.)

Preferred path is **direct/LAN** when reachable; the relay is the fallback.

## What "it only sees ciphertext" does and does not mean

It is a statement about **confidentiality only**. A relay operator — or anyone who reaches the relay —
still controls **availability**, and can **record** the stream. Recording matters because the transport
has no replay protection yet, so a recorded client→daemon stream can be replayed at the real daemon
later (`docs/security-interception-review.md` §4.3).

The `serverID` a host registers with is the daemon's **public** key: printed on daemon start, embedded
in every pairing QR, and in the relay's own request logs. It was never a secret, so registration keyed
on it alone was effectively unauthenticated — anyone who had seen it could evict the real daemon and
take the bridge position. A host now proves possession of the matching **private** key before it can
displace a daemon that has done the same (`daemon/relay/pop.go`). The proof is opt-in (`?pop=1`) so
daemons predating it keep working; they keep the old newest-registration-wins behaviour, and with it
the old exposure, until they update.
