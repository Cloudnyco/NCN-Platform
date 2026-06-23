#!/usr/bin/env python3
# import.py — one-off seed of the self-hosted wiki from the repo markdown.
# Emits SQL (dollar-quoted) to stdout; pipe into psql on the primary:
#   python3 deploy/wiki/import.py | ssh root@deploy-host 'sudo -u postgres psql ncn'
# Idempotent: INSERT ... ON CONFLICT(path) DO UPDATE. Markdown is stored AS-IS
# (admonitions/tables/code rendered by WikiMarkdown.vue). Relative .md links are
# rewritten to absolute wiki paths (/ops/.., /public/.., /home).
import os, re, sys

ROOT = os.path.join(os.path.dirname(__file__), '..', '..', 'wiki')
ROOT = os.path.abspath(ROOT)

# file -> (wiki path, is_public, sort)
def build_map():
    m = {}
    m[f"{ROOT}/public/docs/index.md"] = ("home", True, 0)
    for i, n in enumerate(["asn", "bgp", "igp", "network", "peering", "looking-glass", "status"], start=1):
        m[f"{ROOT}/public/docs/{n}.md"] = (f"public/{n}", True, i)
    m[f"{ROOT}/internal/docs/index.md"] = ("ops/home", False, 10)
    m[f"{ROOT}/internal/docs/reference.md"] = ("ops/reference", False, 11)
    for i, f in enumerate(sorted(os.listdir(f"{ROOT}/internal/docs/systems")), start=20):
        if f.endswith(".md"):
            m[f"{ROOT}/internal/docs/systems/{f}"] = (f"ops/systems/{f[:-3]}", False, i)
    for i, f in enumerate(sorted(os.listdir(f"{ROOT}/internal/docs/runbooks")), start=40):
        if f.endswith(".md"):
            m[f"{ROOT}/internal/docs/runbooks/{f}"] = (f"ops/runbooks/{f[:-3]}", False, i)
    return m

FMAP = build_map()

def title_of(text, path):
    mo = re.search(r'^#\s+(.+)$', text, re.M)
    return mo.group(1).strip() if mo else path.split("/")[-1]

def rewrite_links(text, srcfile):
    srcdir = os.path.dirname(srcfile)
    def repl(mo):
        rel = mo.group(1)
        # The regex captures the target WITHOUT its ".md" suffix, so add it back
        # before resolving — FMAP is keyed by the real ".md" file path. (Without
        # this, nothing ever matched and links shipped as raw "peering.md",
        # which the renderer then navigated to → backend "invalid path".)
        absf = os.path.normpath(os.path.join(srcdir, rel + ".md"))
        if absf in FMAP:
            return f"](/{FMAP[absf][0]})"
        return mo.group(0)
    return re.sub(r'\]\(([^)#]*?)\.md(?:#[^)]*)?\)', repl, text)

DQ = "$WIKI$"  # dollar-quote tag; markdown never contains it

def emit():
    print("BEGIN;")
    for f, (path, is_public, sort) in sorted(FMAP.items(), key=lambda kv: kv[1][2]):
        text = rewrite_links(open(f).read(), f)
        if DQ in text or DQ in path:
            sys.exit(f"dollar-quote tag collision in {f}")
        title = title_of(text, path)
        pub = "true" if is_public else "false"
        print(
            f"INSERT INTO wiki_pages(path,title,content,locale,is_public,sort,updated_by,updated_at,version,created_at) "
            f"VALUES ('{path}',{DQ}{title}{DQ},{DQ}{text}{DQ},'zh',{pub},{sort},'import',now(),1,now()) "
            f"ON CONFLICT(path) DO UPDATE SET title=EXCLUDED.title, content=EXCLUDED.content, "
            f"is_public=EXCLUDED.is_public, sort=EXCLUDED.sort, updated_at=now();"
        )
    print("COMMIT;")
    print(f"-- {len(FMAP)} pages", file=sys.stderr)

if __name__ == "__main__":
    emit()
