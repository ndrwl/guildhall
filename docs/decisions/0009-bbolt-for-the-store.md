# D9. bbolt for the store

The store (R1.17) uses `go.etcd.io/bbolt` v1.5.0, which brings `golang.org/x/sys`.
It is pure Go, so release builds keep `CGO_ENABLED=0`; it gives serializable transactions with a durable commit, and its kernel file lock works on Linux, Windows and macOS.
Records are JSON from the standard library inside bbolt buckets, so the store needs no other codec.
The store has no generic storage interface: `internal/store` exposes concrete record types and mutation methods only.
