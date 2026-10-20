# D20. Typing the launch command into a new tab

Herdr's tab creation does not take a command, so Guildhall creates the tab, waits until its shell is idle, and types one quoted command line with `pane run`.
Quoting is chosen from the shell Herdr reports for the pane: POSIX single quoting or PowerShell's call operator with single quotes.
An unrecognised shell is refused rather than guessed.
Rendered lines are proven by executing them in each supported shell installed on the test host, and an argument a shell would alter is refused rather than changed.
