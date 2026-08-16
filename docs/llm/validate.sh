#!/usr/bin/env bash
# docs/llm/validate.sh - llm-docs v3 structural validation + staleness report.
# Read-only. Errors (exit 1): broken links, missing/malformed frontmatter,
# missing touches paths, TODO task without Accept, checked task without
# Evidence, invalid PRD status, top-level file not in 00_index.md.
# STALE/WARN lines are a report only (exit stays 0).
set -uo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -z "$ROOT" ] && ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
LLM=docs/llm
errors=0
warns=0
stales=0

err()  { echo "ERROR: $*"; errors=$((errors+1)); }
warn() { echo "WARN: $*";  warns=$((warns+1)); }

[ -d "$LLM" ] || { echo "ERROR: $LLM not found"; exit 1; }

# --- frontmatter helpers ----------------------------------------------------
fm_value() { # $1=file $2=key -> first inline value
  [ "$(head -1 "$1")" = "---" ] || return 0
  sed -n '2,/^---$/p' "$1" | awk -F': *' -v k="$2" '$1==k{print $2; exit}'
}
fm_touches() { # $1=file -> paths, one per line (inline list or YAML list)
  [ "$(head -1 "$1")" = "---" ] || return 0
  sed -n '2,/^---$/p' "$1" | awk '
    /^touches:/ { intouch=1; sub(/^touches:[ \t]*/, ""); gsub(/,[ \t]*/, "\n");
                  if (length($0)) print; next }
    intouch && /^[ \t]+-/ { sub(/^[ \t]*-[ \t]*/, ""); print; next }
    intouch && /^[^ \t#]/ { intouch=0 }
  '
}

# --- 1. link check -----------------------------------------------------------
while IFS= read -r -d '' doc; do
  doc_dir="$(dirname "$doc")"
  while IFS= read -r target; do
    case "$target" in
      http://*|https://*|mailto:*|'#'*) continue ;;
      '') continue ;;
    esac
    path="${target%%#*}"
    [ -z "$path" ] && continue
    [ -e "$doc_dir/$path" ] || err "$doc: broken link -> $target"
  done < <(grep -o '\[[^]]*\]([^)]*)' "$doc" | sed 's/^\[[^]]*\](//; s/)$//')
done < <(find "$LLM" -name '*.md' -print0)

# --- 2. index sync -----------------------------------------------------------
idx="$LLM/00_index.md"
[ -f "$idx" ] || err "00_index.md missing"
if [ -f "$idx" ]; then
  for f in "$LLM"/*.md; do
    [ -e "$f" ] || continue
    b="$(basename "$f")"
    [ "$b" = "00_index.md" ] && continue
    grep -qF "$b" "$idx" || err "$b not listed in 00_index.md"
  done
fi

# --- 3-5. spec frontmatter, touches existence, staleness ---------------------
is_date() { [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; }

for f in "$LLM"/[0-9][0-9]_*.md; do
  [ -e "$f" ] || continue
  case "$(basename "$f")" in 00_index.md|90_lessons.md) continue ;; esac
  if [ "$(head -1 "$f")" != "---" ]; then
    err "$f: no frontmatter (need id/kind/touches/written/updated)"
    continue
  fi
  upd="$(fm_value "$f" updated)"
  wrt="$(fm_value "$f" written)"
  is_date "$upd" || err "$f: updated missing or not YYYY-MM-DD (got '${upd:-none}')"
  is_date "$wrt" || err "$f: written missing or not YYYY-MM-DD (got '${wrt:-none}')"

  tcount=0
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    tcount=$((tcount+1))
    if [ ! -e "$p" ]; then
      err "$f: touches missing path: $p"
      continue
    fi
    last="$(git log -1 --format=%cI -- "$p" 2>/dev/null | cut -dT -f1)"
    if [ -z "$last" ]; then
      warn "$f: touches path with no git history (uncommitted?): $p"
      continue
    fi
    if is_date "$upd" && [[ "$last" > "$upd" ]]; then
      echo "STALE: $f - $p last commit $last > updated $upd"
      stales=$((stales+1))
    fi
  done < <(fm_touches "$f")
  [ "$tcount" -gt 0 ] || err "$f: touches empty - name the code paths this doc governs"
done

# --- 6. TODO discipline ------------------------------------------------------
if [ -f "$LLM/TODO.md" ]; then
  while IFS= read -r line; do
    err "TODO.md: ${line#ERR }"
  done < <(awk '
    function flush() {
      if (task != "" && acc == 0)
        print "ERR task without Accept: " task
      if (task != "" && done && ev == 0)
        print "ERR done task without Evidence: " task
    }
    /^[-*] \[[ xX]\]/ {
      flush(); task=$0; acc=0; ev=0
      done = ($0 ~ /\[[xX]\]/) ? 1 : 0; next
    }
    /^[ \t]*Accept:/   { if (task != "") acc=1 }
    /^[ \t]*Evidence:/ { if (task != "") ev=1 }
    /^#/ { flush(); task="" }
    END { flush() }
  ' "$LLM/TODO.md")
fi

# --- 7. PRD status -----------------------------------------------------------
if [ -f "$LLM/PRD.md" ]; then
  st="$(awk -F': *' '/^Status:/{print $2; exit}' "$LLM/PRD.md")"
  if [ "$st" = "DRAFT" ]; then :
  elif [[ "$st" =~ ^FROZEN\ [0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then :
  else
    err "PRD.md Status: must be 'DRAFT' or 'FROZEN YYYY-MM-DD' (got '${st:-none}')"
  fi
fi

# --- summary ------------------------------------------------------------------
echo "----"
echo "validate: $errors error(s), $warns warn(s), $stales stale spec(s)"
[ "$errors" -eq 0 ]
