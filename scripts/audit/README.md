# Audit / Certification Kit (`make audit`)

Re-runnable hygiene scan used to certify the repository before publishing and
re-verified after the history reset (see kanban PB-142 / B2).

## What it scans

`scripts/audit/scan.sh` over tracked + untracked-but-not-ignored files, plus
git metadata:

- **Secret battery** (generic shapes): private-key blocks, GitHub/PAT-style
  tokens, `sk-…` keys, JWTs, AWS key ids, DB connection strings with embedded
  credentials, generic `token/secret/password/api-key` assignments.
- **Identity battery** (generic): emails, IPv4/IPv6, MACs, `/home/*`,
  `/mnt/*`, Windows WSL network paths (backslash-wsl-dot-localhost), Windows
  profile paths (backslash-Users-backslash wildcard), URLs with credentials.
- **Overlay battery**: case-insensitive fixed strings from
  `identities.local.txt` (local-only, git-ignored). Absent overlay → warning.
- **Git metadata**: every author/committer identity must be in the allowlist
  (default `mandusm <mandusm@users.noreply.github.com>`, override with
  `AUTHOR_ALLOWLIST='Name <email> …'`); `git fsck --dangling` count, reflog /
  stash presence warnings.
- **Artifact battery**: tracked `bin/`, `*.log`, `*.sqlite*`, `*.db`,
  `.vscode/`, `dist/`, `coverage/`, `.env*` files.
- **External tools**: `gitleaks` / `trufflehog` auto-detected; skipped with a
  loud warning if missing (`AUDIT_SKIP_LEAK=1` to silence detection).

## Running

```sh
make audit            # exit non-zero on any MUST-FIX finding
scripts/audit/scan.sh # directly; optional arg = repo root
```

Output: compact per-category counts + `file:lines` locations (values are
never printed). Exit code: `0` clean, `1` MUST-FIX findings, `2` setup error.

## Adding overlay terms

Edit `scripts/audit/identities.local.txt` (local machine only — it is NOT
committed): one case-insensitive fixed string per line, `#` comments allowed.
Never put real identity terms in `scan.sh`, the README, or any committed file.

## CI reuse

The script is dependency-light (bash + git + grep). In CI: run
`scripts/audit/scan.sh "$GITHUB_WORKSPACE"` with `AUDIT_STRICT_OVERLAY=1` to
fail loudly when the overlay is missing, and install gitleaks for full secret
coverage. Gating on exit code is enough.

## Note for DOC2

Reference `make audit` from `AGENTS.md` as the standard pre-publish
verification step.
