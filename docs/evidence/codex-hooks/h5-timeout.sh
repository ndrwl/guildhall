#!/bin/sh
# H5: hook that never answers within its timeout
cat > /dev/null
sleep 20
exit 0
