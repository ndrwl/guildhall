#!/bin/sh
# H5: hook that exits 0 but prints malformed output
cat > /dev/null
echo '{"hookSpecificOutput": {"permissionDecision": "deny"'
exit 0
