# D11. Repository paths are absolute in the owner machine's form

A Steward validates Director configs whose owner machine may run another OS than its own, so `internal/config` accepts a repository `path` that starts with `/` or with a drive letter followed by `/` or `\`, on every host.
Relative, drive-relative and backslash-prefixed UNC paths are refused; paths beginning with `/`, including `//`, are treated as POSIX paths.
Duplicate comparison cleans POSIX dot segments and, for drive paths, normalizes separators, drive-letter case and dot segments independently of the validating host.
A later Director layer that opens repositories on its own machine checks the path against that machine's form.
