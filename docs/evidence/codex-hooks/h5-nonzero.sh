#!/bin/sh
# H5: hook that exits non-zero (1) with no stdout
cat > /dev/null
echo "h5-nonzero: failing on purpose" >&2
exit 1
