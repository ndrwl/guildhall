# D6. `doctor` reports only what it checked

The user-facing `guildhall doctor` report is described in the README.
A missing tool is reported, not treated as a failure, so `doctor` exits 0 whenever it could write its report.
Each version command is bounded by a timeout.
