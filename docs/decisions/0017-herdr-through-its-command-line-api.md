# D17. Herdr through its command-line API

Guildhall drives Herdr by running the `herdr` client with argument lists and reading the JSON it prints, as Herdr's own agent guide documents.
No Go client library for Herdr was found, and the client already handles each platform's transport (a Unix socket, or a named pipe on Windows), so Guildhall does not implement the socket protocol.
Responses recorded from Herdr 0.9.5 are kept under `internal/herdr/testdata` so tests run without Herdr installed.
