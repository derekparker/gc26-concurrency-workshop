#!/bin/bash
# Validate the presenter slide decks.
#
# Checks that every .slide file parses, that every file it references exists,
# and that no Markdown table syntax leaked in (present's Markdown renderer has
# no table extension, so pipe tables render as literal text).

echo "🖼  Slide Deck Checker"
echo "====================="

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

FAILURES=0

fail() {
    echo "   ❌ $1"
    FAILURES=$((FAILURES + 1))
}

# --- is the present tool available? -----------------------------------------
if ! command -v present > /dev/null 2>&1; then
    echo
    echo "present not found. Install it with:"
    echo "    go install golang.org/x/tools/cmd/present@latest"
    echo "(then make sure \$(go env GOPATH)/bin is on your PATH)"
    exit 1
fi

DECKS=$(find . -name '*.slide' -not -path './.git/*' | sort)
if [ -z "$DECKS" ]; then
    echo "No .slide files found."
    exit 1
fi

# --- static checks per deck --------------------------------------------------
for deck in $DECKS; do
    echo
    echo "$deck"
    dir=$(dirname "$deck")

    # Markdown mode requires "# " (hash + space) on the first line. Without the
    # space the file silently parses as legacy present, where # is a comment.
    if ! head -1 "$deck" | grep -q '^# '; then
        fail "first line must start with '# ' to enable Markdown mode"
    fi

    # Referenced files must exist, relative to the deck's own directory.
    while read -r ref; do
        [ -z "$ref" ] && continue
        if [ ! -f "$dir/$ref" ]; then
            fail "missing referenced file: $ref"
        fi
    done < <(grep -oE '^\.(html|image|background) +[^ ]+' "$deck" | awk '{print $2}')

    # .code targets, ignoring the -edit/-numbers flags and any address suffix.
    while read -r ref; do
        [ -z "$ref" ] && continue
        if [ ! -f "$dir/$ref" ]; then
            fail "missing .code source: $ref"
        fi
    done < <(grep -oE '^\.(code|play) +(-(edit|numbers) +)*[^ ]+' "$deck" \
             | sed -E 's/^\.(code|play) +//; s/-(edit|numbers) +//g')

    # Pipe tables would render as literal text.
    if grep -qE '^\|.*\|' "$deck"; then
        fail "Markdown pipe table found — present has no table extension; use an .html figure"
    fi

    # Any line starting with '.' is parsed as a directive, so prose beginning
    # with "...", ".NET", etc. is a hard parse error.
    while read -r line; do
        [ -z "$line" ] && continue
        fail "line starts with '.' but is not a directive: ${line:0:60}"
    done < <(grep -E '^\.' "$deck" \
             | grep -vE '^\.(code|play|link|image|video|caption|iframe|html|background) ')

    slides=$(grep -c '^## ' "$deck")
    notes=$(grep -c '^: ' "$deck")
    echo "   $slides slides, $notes speaker-note lines"
done

# --- does present actually parse them? ---------------------------------------
echo
echo "Parsing with present..."
PORT=4998
present -notes -play=false -http="127.0.0.1:$PORT" . > /tmp/present-check.log 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null' EXIT

for _ in $(seq 1 20); do
    curl -s -o /dev/null "http://127.0.0.1:$PORT/" && break
    sleep 0.25
done

for deck in $DECKS; do
    path=${deck#./}
    code=$(curl -s -o /tmp/slide-check.html -w '%{http_code}' "http://127.0.0.1:$PORT/$path")
    if [ "$code" != "200" ]; then
        fail "$path did not render (HTTP $code)"
        grep -i "$(basename "$deck")" /tmp/present-check.log | tail -2
    else
        articles=$(grep -c '<article' /tmp/slide-check.html)
        echo "   ✅ $path — $articles articles rendered"
    fi
done

echo
if [ "$FAILURES" -eq 0 ]; then
    echo "All decks OK."
    echo "Present them with:  present -notes -play=false   (then press N for notes)"
else
    echo "$FAILURES problem(s) found."
fi
exit $((FAILURES > 0))
