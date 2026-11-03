#!/bin/sh
# H5 (extra): Claude-style block, exit 2 with the reason on stderr
cat > /dev/null
echo "h5-exit2: blocked by exit code 2" >&2
exit 2
