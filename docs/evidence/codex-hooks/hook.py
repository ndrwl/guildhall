#!/usr/bin/env python3
# Throwaway d108 spike hook: allow a fixed list, deny everything else.
# Usage: hook.py <policy-file> <log-dir>
import json, os, shlex, sys, time

policy_path, log_dir = sys.argv[1], sys.argv[2]
raw = sys.stdin.read()
os.makedirs(log_dir, exist_ok=True)
stamp = "%.6f-%d" % (time.time(), os.getpid())
with open(os.path.join(log_dir, "payload-" + stamp + ".json"), "w") as f:
    f.write(raw)

def decide():
    try:
        p = json.loads(raw)
    except Exception as e:
        return "deny", "unparseable payload: %s" % e
    tool = p.get("tool_name", "")
    ti = p.get("tool_input") or {}
    cmd = ti.get("command") if isinstance(ti, dict) else None
    if tool not in ("Bash", "shell", "exec_command", "local_shell", "unified_exec") or cmd is None:
        return "deny", "tool %r is not on the allow-list" % tool
    if isinstance(cmd, list):
        argv = cmd
    else:
        if any(op in cmd for op in [";", "&", "|", "`", "$(", ">", "<", "\n"]):
            return "deny", "shell control operators are not allowed: %r" % cmd
        try:
            argv = shlex.split(cmd)
        except ValueError as e:
            return "deny", "unparseable command: %s" % e
    exact, prefixes = [], []
    for line in open(policy_path):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        kind, _, rest = line.partition(" ")
        (exact if kind == "exact" else prefixes).append(shlex.split(rest))
    if argv in exact:
        return "allow", "exact rule"
    for pre in prefixes:
        if argv[: len(pre)] == pre:
            return "allow", "prefix rule %s" % " ".join(pre)
    return "deny", "command %r is not on the allow-list" % (cmd,)

decision, reason = decide()
with open(os.path.join(log_dir, "decisions.log"), "a") as f:
    f.write("%s %s %s\n" % (stamp, decision, reason))
if decision == "deny":
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": "guildhall spike: " + reason}}))
sys.exit(0)
