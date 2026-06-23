#!/usr/bin/env python3
# Build Wiki.js pages.create requests (ndjson) from the MkDocs markdown, with
# two transforms so it renders cleanly in Wiki.js (standard markdown-it):
#   1. link rewrite: ../foo.md → /ops/foo (absolute Wiki.js paths)
#   2. admonition transform: `!!! type "title"` + indented body → blockquote
import json, re, os

ROOT = "/root/ncn-workspace/core-console/wiki"

def build_map():
    m = {}
    m[f"{ROOT}/public/docs/index.md"] = "home"
    for n in ["network","peering","looking-glass","status"]:
        m[f"{ROOT}/public/docs/{n}.md"] = f"public/{n}"
    m[f"{ROOT}/internal/docs/index.md"] = "ops/home"
    m[f"{ROOT}/internal/docs/reference.md"] = "ops/reference"
    for sub in ["systems","runbooks"]:
        d = f"{ROOT}/internal/docs/{sub}"
        for f in os.listdir(d):
            if f.endswith(".md"):
                m[f"{d}/{f}"] = f"ops/{sub}/{f[:-3]}"
    return m

FMAP = build_map()
EMOJI = {"danger":"🚨","warning":"⚠️","note":"📝","tip":"💡","info":"ℹ️","example":"📋","caution":"⚠️"}

def title_of(text, path):
    mo = re.search(r'^#\s+(.+)$', text, re.M)
    return mo.group(1).strip() if mo else path.split("/")[-1]

def rewrite_links(text, srcfile):
    srcdir = os.path.dirname(srcfile)
    def repl(mo):
        rel = mo.group(1)
        absf = os.path.normpath(os.path.join(srcdir, rel))
        wp = FMAP.get(absf)
        return f"](/{wp})" if wp else mo.group(0)
    return re.sub(r'\]\(([^)#]*?)\.md(?:#[^)]*)?\)', repl, text)

def transform_admonitions(text):
    lines = text.split("\n")
    out = []
    i = 0
    adm = re.compile(r'^(\s*)!!!\s+(\w+)(?:\s+"([^"]*)")?\s*$')
    while i < len(lines):
        m = adm.match(lines[i])
        if not m:
            out.append(lines[i]); i += 1; continue
        indent, typ, title = m.group(1), m.group(2).lower(), m.group(3)
        head = title or typ.capitalize()
        emoji = EMOJI.get(typ, "📌")
        # collect indented body (more indented than the marker), allowing blank lines
        body = []
        j = i + 1
        base = len(indent)
        while j < len(lines):
            ln = lines[j]
            if ln.strip() == "":
                body.append(""); j += 1; continue
            ind = len(ln) - len(ln.lstrip(" "))
            if ind > base:
                body.append(ln[base+4:] if len(ln) >= base+4 else ln.lstrip(" "))
                j += 1
            else:
                break
        # trim trailing blanks in body
        while body and body[-1] == "":
            body.pop()
        out.append(f"> **{emoji} {head}**")
        out.append(">")
        for b in body:
            out.append("> " + b if b else ">")
        out.append("")
        i = j
    return "\n".join(out)

QUERY = ("mutation($content:String!,$title:String!,$path:String!,$desc:String!){"
         "pages{create(content:$content,description:$desc,editor:\"markdown\","
         "isPublished:true,isPrivate:false,locale:\"zh\",path:$path,tags:[],title:$title)"
         "{responseResult{succeeded errorCode message}}}}")

out = open("/tmp/wiki-import.ndjson","w")
for f, wp in sorted(FMAP.items(), key=lambda kv: kv[1]):
    text = open(f).read()
    title = title_of(text, wp)
    text = rewrite_links(text, f)
    text = transform_admonitions(text)
    desc = (re.sub(r'[#*`>\-]', '', re.sub(r'^#.*$','',text,flags=re.M)).strip().split("\n")[0] or title)[:180]
    out.write(json.dumps({"query":QUERY,"variables":{"content":text,"title":title,"path":wp,"desc":desc}}, ensure_ascii=True)+"\n")
out.close()
print(f"wrote {len(FMAP)} pages with admonition+link transforms")
