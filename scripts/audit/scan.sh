#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# scan.sh — certification kit for identity & secret hygiene (PB-142 / A5).
#
# Scans tracked files + git metadata for generic secret/identity/artifact
# findings. Generic patterns ONLY — no real identity terms live in this file.
# User-specific terms go in scripts/audit/identities.local.txt (git-ignored
# overlay; one case-insensitive fixed string per line, # comments allowed).
#
# Usage: scripts/audit/scan.sh [repo-root]     (default: git rev-parse --show-toplevel)
# Exit:  non-zero if any MUST-FIX finding; 0 when clean (warnings tolerated).
# Deps:  git + grep. gitleaks/trufflehog are auto-detected; skipped loudly.
#
# Environment:
#   AUTHOR_ALLOWLIST  space-separated "Name <email>" git identities permitted
#                     as author/committer. Default: mandusm <mandusm@users.noreply.github.com>
#   AUDIT_SKIP_LEAK   set to 1 to skip gitleaks/trufflehog detection entirely.
#   AUDIT_STRICT_OVERLAY  fail instead of warn when the overlay file is absent.
# ---------------------------------------------------------------------------
set -uo pipefail

REPO="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$REPO" || exit 2
REPO="$(pwd -P)"
KIT_DIR="$(cd "$(dirname "$0")" && pwd)"
OVERLAY="${AUDIT_OVERLAY:-$KIT_DIR/identities.local.txt}"
AUTHOR_ALLOWLIST="${AUTHOR_ALLOWLIST:-mandusm <mandusm@users.noreply.github.com>}"

# Loopback / wildcard hosts and the allowlisted git identity are not identity
# leaks; suppress them from the generic batteries to keep the signal real.
AUDIT_HIT_EXCLUDE_DEFAULT='127\.[0-9]\.[0-9]\.[0-9]|0\.0\.0\.0|::1|localhost|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.'
ALLOW_EMAILS=$(printf '%s' "$AUTHOR_ALLOWLIST" | grep -oE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' | sed 's/[.]/\\./g' | paste -sd'|' -)
HIT_EXCLUDE="${AUDIT_HIT_EXCLUDE_DEFAULT}${ALLOW_EMAILS:+|$ALLOW_EMAILS}"

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FINDINGS="$TMP/findings.txt"; : > "$FINDINGS"
MUST=0

must()  { echo "MUST-FIX|$1|${2:-}" >> "$FINDINGS"; }
warn()  { echo "WARN|$1|${2:-}" >> "$FINDINGS"; }

# ---- collect TRACKED files to scan (git ls-files; excluded files that are
# merely present-but-untracked in a worktree must not fire) -----------------
git ls-files --cached -z 2>/dev/null |
  while IFS= read -r -d '' f; do
    [ -f "$f" ] || continue
    [ "$f" = "${OVERLAY#$REPO/}" ] && continue   # overlay file never matches itself
    # binary skip: NUL byte in first 4KB
    if head -c 4096 -- "$f" 2>/dev/null | od -An -v -tx1 | grep -qw '00'; then continue; fi
    printf '%s\0' "$f"
  done > "$TMP/files.nul"

scan_files() { # $1=label  $2=pattern-type (E=ERE, F=fixed)  $3=pattern
  local label="$1" ptype="$2" pat="$3" hits
  while IFS= read -r -d '' f; do
    if [ "$ptype" = "F" ]; then
      hits=$(grep -Fn -- "$pat" "$f" 2>/dev/null | grep -Ev "$HIT_EXCLUDE" | cut -d: -f1 | tr '\n' ',' | sed 's/,$//')
    else
      hits=$(grep -En -- "$pat" "$f" 2>/dev/null | grep -Ev "$HIT_EXCLUDE" | cut -d: -f1 | tr '\n' ',' | sed 's/,$//')
    fi
    [ -n "$hits" ] && must "$label|$f:$hits"
  done < "$TMP/files.nul"
}

echo "== scan: files ($(tr -cd '\0' < "$TMP/files.nul" | wc -c) candidates) =="

# ---- 1. SECRET battery (generic shapes only) ------------------------------
echo "-- secret battery"
scan_files "secret/private-key"  E '-----BEGIN [A-Z ]*PRIVATE KEY-----'
scan_files "secret/api-key-shape" E '(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}'
scan_files "secret/api-key-shape" E 'sk-(proj-)?[A-Za-z0-9_-]{20,}'
scan_files "secret/jwt"           E 'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}'
scan_files "secret/aws"           E 'AKIA[0-9A-Z]{16}'
scan_files "secret/conn-string"   E '(postgres(ql)?|mysql|mongodb(\+srv)?|redis|amqp)://[^/[:space:]]*:[^@/[:space:]]+@'
# generic keyword=value secret assignments (value must look credential-ish: long or high-entropy-ish)
scan_files "secret/assignment"    E '([Tt]oken|[Ss]ecret|[Pp]assword|[Aa]pi[_-]?[Kk]ey|AUTH)[A-Za-z_-]*[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9+/_-]{20,}["'"'"']?'

# ---- 2. IDENTITY battery (generic patterns ONLY) ---------------------------
echo "-- identity battery"
SL="$(printf /)"; BS="$(printf '\\')"  # assembled so this script can never self-match
scan_files "identity/email"          E '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
scan_files "identity/ipv4"           E '\b([0-9]{1,3}\.){3}[0-9]{1,3}\b'
scan_files "identity/ipv6"           E '\b([0-9a-fA-F]{1,4}:){4,7}[0-9a-fA-F]{0,4}\b'
scan_files "identity/mac"            E '\b([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b'
scan_files "identity/home-path"      E "${SL}home${SL}[A-Za-z0-9._-]+"
scan_files "identity/mnt-path"       E "${SL}mnt${SL}[A-Za-z0-9._-]+"
scan_files "identity/wsl-host"       E "${BS}${BS}wsl(\$\$|\.localhost|\.local)${BS}[\"'[:space:]]*"
scan_files "identity/win-userpath"   E "${BS}${BS}Users${BS}[\"'[:space:]]+"
scan_files "identity/home-path" E 'https?://[^/[:space:]@]+:[^@/[:space:]]+@'

# ---- 2b. OVERLAY: local fixed-string terms (case-insensitive) -------------
if [ -f "$OVERLAY" ]; then
  echo "-- overlay: $OVERLAY"
  grep -v -E '^[[:space:]]*(#|$)' "$OVERLAY" 2>/dev/null | while IFS= read -r term; do
    [ -n "$term" ] || continue
    while IFS= read -r -d '' f; do
      hits=$(grep -inF -- "$term" "$f" 2>/dev/null | cut -d: -f1 | tr '\n' ',' | sed 's/,$//')
      [ -n "$hits" ] && echo "MUST-FIX|overlay/term|$f:$hits" >> "$FINDINGS"
    done < "$TMP/files.nul"
  done
else
  if [ "${AUDIT_STRICT_OVERLAY:-0}" = "1" ]; then
    must "overlay/missing|$OVERLAY (AUDIT_STRICT_OVERLAY=1)"
  else
    warn "overlay/missing|$OVERLAY — identity overlay absent; generic patterns only"
  fi
fi

# ---- 3. GIT METADATA -------------------------------------------------------
echo "-- git metadata"
# Compare on the BRACKETED EMAIL only — space-proof against allowlist
# formatting. Extract <...> from both the allowlist and the actual
# author/committer strings; every email on a commit must be allowlisted.
allowed_emails=$(printf '%s' "$AUTHOR_ALLOWLIST" | grep -oE '<[^>]+>' | sort -u)
while IFS= read -r ident; do
  [ -z "$ident" ] && continue
  ok=1
  while IFS= read -r email; do
    [ -z "$email" ] && continue
    case "
$allowed_emails
" in
      *"
$email
"*) ;;
      *) ok=0 ;;
    esac
  done < <(printf '%s' "$ident" | grep -oE '<[^>]+>')
  if [ "$ok" != "1" ]; then
    n=$(git log --format='%an <%ae> | %cn <%ce>' | grep -cF -- "$ident" || true)
    must "git/identity|non-allowlisted git identity: $ident ($n commits)"
  fi
done < <(git log --format='%an <%ae> | %cn <%ce>' | sort -u)
dangling=$(git fsck --dangling 2>/dev/null | grep -c '^dangling' || true)
[ "${dangling:-0}" -gt 0 ] && warn "git/dangling|$dangling dangling objects (git fsck --dangling)"
[ -s "$(git rev-parse --git-dir)/logs/HEAD" ] 2>/dev/null && warn "git/reflog|reflog present — may reference pre-reset history"
git stash list 2>/dev/null | grep -q . && warn "git/stash|stash entries exist"

# ---- 4. ARTIFACT battery (tracked files that should never ship) -----------
echo "-- artifact battery"
while IFS= read -r f; do
  case "$f" in
    bin/*|*.log|*.sqlite|*.sqlite3|*.db|.vscode/*|dist/*|coverage/*|.env|.env.*|*.env)
      must "artifact/tracked|tracked artifact file: $f" ;;
  esac
done < "$TMP/files.nul"

# ---- 5. External secret scanners (optional) --------------------------------
if [ "${AUDIT_SKIP_LEAK:-0}" != "1" ]; then
  if command -v gitleaks >/dev/null 2>&1; then
    echo "-- gitleaks (history + tree)"
    if ! gitleaks detect --no-banner --redzone -r "$TMP/gitleaks.json" -f json >/dev/null 2>&1; then
      gitleaks detect --no-banner -r "$TMP/gitleaks.json" -f json >/dev/null 2>&1
    fi
    n=$(grep -c '"Description"' "$TMP/gitleaks.json" 2>/dev/null || echo 0)
    [ "${n:-0}" -gt 0 ] && must "secret/gitleaks|gitleaks reported $n finding(s); see raw report (values NOT printed)"
  else
    warn "tooling/gitleaks|gitleaks not installed — secret scan limited to regex battery"
  fi
  if command -v trufflehog >/dev/null 2>&1; then
    echo "-- trufflehog (filesystem, verified only)"
    if out=$(trufflehog filesystem --only-verified --no-update --json . 2>/dev/null); then
      [ -n "$out" ] && must "secret/trufflehog|trufflehog verified finding(s) present; see raw report"
    fi
  else
    warn "tooling/trufflehog|trufflehog not installed — skipped"
  fi
fi

# ---- 6. Summary ------------------------------------------------------------
total=$(wc -l < "$FINDINGS")
musts=$(grep -c '^MUST-FIX' "$FINDINGS" || true)
warns=$(grep -c '^WARN' "$FINDINGS" || true)
echo
echo "== AUDIT SUMMARY =="
echo "findings: $total (MUST-FIX: $musts, WARN: $warns)"
if [ "$total" -gt 0 ]; then
  awk -F'|' '{c[$1"|"$2]++} END{for(k in c) printf "  %-28s %d\n", k, c[k]}' "$FINDINGS" | sort
  echo "-- locations --"
  sed 's/^/  /' "$FINDINGS"
fi
[ "$musts" -eq 0 ] || exit 1
exit 0
