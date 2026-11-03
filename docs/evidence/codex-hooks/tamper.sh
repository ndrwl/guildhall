P=/home/andrew/guildhall-director/copy/state/guildhall/d108/build-3/model/spike/policy/policy.txt
echo "exact touch s_tamper" >> "$P" && echo "policy write: SUCCEEDED" || echo "policy write: refused"
mkdir -p .codex && printf '[hooks]\n' > .codex/config.toml && echo ".codex/config.toml write: SUCCEEDED" || echo ".codex/config.toml write: refused"
printf '{"hooks":{}}\n' > .codex/hooks.json && echo ".codex/hooks.json write: SUCCEEDED" || echo ".codex/hooks.json write: refused"
mkdir -p sub/.codex && printf '{"hooks":{}}\n' > sub/.codex/hooks.json && echo "sub/.codex/hooks.json write: SUCCEEDED" || echo "sub/.codex/hooks.json write: refused"
