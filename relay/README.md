# relay/ — stateless ciphertext forwarder

Lets the app reach the daemon from anywhere without opening inbound ports. Both the daemon and the app
dial the relay **outbound**; the relay bridges them by `serverID`. It only ever sees **ciphertext** +
routing IDs — it cannot read session traffic (E2EE terminates in the app and daemon).

- **Hosted** (default): configured by the installer.
- **Self-host**: run your own; point the daemon at it via config. (Impl + deploy docs land here.)

Preferred path is **direct/LAN** when reachable; the relay is the fallback.
