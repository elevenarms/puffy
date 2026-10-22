#!/usr/bin/env python3
"""Build puffy's knowledge base: OpenBSD amd64 man pages and packages.

One-time job on the build machine (a Mac). Renders every man page with
mandoc, splits it into sections, reads the package catalog from sqlports,
embeds every chunk with a local llama-server running nomic-embed-text-v1.5,
and writes one SQLite file with an FTS5 index and a sqlite-vector column.

    python3 kb/build_kb.py --man build/man --sqlports build/sqlports/... \
        --index build/dl/pkg-index.txt --vector build/sqlite-vector/dist/vector \
        --embed-url http://127.0.0.1:8089 --out data/puffy-kb.db
"""

import argparse
import hashlib
import json
import math
import os
import re
import sqlite3
import struct
import subprocess
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor

ARCH = "amd64"
DIMENSION = 768
VECTOR_TYPE = "FLOAT16"   # full vectors, kept for exact re-ranking
QUANTIZATION = "TURBO4"   # the scan index; see kb/eval_quant.py
CANDIDATES = 80           # TURBO4 candidates re-ranked by float16 per scan
MAX_CHUNK = 3000      # characters; nomic-embed-text was trained on 2048 tokens
DOCUMENT_PREFIX = "search_document: "
QUERY_PREFIX = "search_query: "

SCHEMA = """
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE docs (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,          -- 'man' or 'pkg'
    name TEXT NOT NULL,          -- pf.conf, ifconfig, or a package stem
    section TEXT NOT NULL,       -- man section (5, 8, 3p) or pkg path
    heading TEXT NOT NULL,       -- man section heading or pkg part
    title TEXT NOT NULL,         -- one-line summary of the page or package
    body TEXT NOT NULL,
    embedding BLOB
);
CREATE INDEX docs_name ON docs(name);
CREATE TABLE pages (             -- one row per man page or package
    kind TEXT NOT NULL, name TEXT NOT NULL, section TEXT NOT NULL,
    title TEXT NOT NULL, aliases TEXT NOT NULL,
    tree TEXT NOT NULL DEFAULT '',   -- base, x11 or pkg
    embedding BLOB,                  -- the "page card": name and title
    PRIMARY KEY (kind, name, section)
);
CREATE VIRTUAL TABLE docs_fts USING fts5(
    name, heading, title, body,
    content='docs', content_rowid='id', tokenize='porter unicode61'
);
"""

SECTION_HEADING = re.compile(r"^[A-Z][A-Z0-9 ,/()&'-]*$")


def main():
    options = parse_arguments()
    if options.add_cards:
        add_cards(options)
        return
    if options.compact:
        compact(options)
        return
    if os.path.exists(options.out):
        os.remove(options.out)
    database = sqlite3.connect(options.out)
    database.executescript(SCHEMA)

    chunks = list(read_man_pages(options.man, database))
    print(f"man chunks: {len(chunks)}", file=sys.stderr)
    package_chunks = list(read_packages(options.sqlports, options.index,
                                        database))
    print(f"package chunks: {len(package_chunks)}", file=sys.stderr)
    chunks += package_chunks

    insert_chunks(database, chunks)
    embed_all(database, options.embed_url, options.workers)
    embed_cards(database, options.embed_url, options.workers)
    finish_index(database, options.vector, options)
    database.close()


def parse_arguments():
    parser = argparse.ArgumentParser()
    parser.add_argument("--man", required=True,
                        help="folder with usr/share/man and usr/X11R6/man")
    parser.add_argument("--sqlports", required=True)
    parser.add_argument("--index", required=True,
                        help="packages/amd64/index.txt from the mirror")
    parser.add_argument("--vector", required=True,
                        help="sqlite-vector extension, without suffix")
    parser.add_argument("--embed-url", default="http://127.0.0.1:8089")
    parser.add_argument("--embed-model", default="nomic-embed-text-v1.5")
    parser.add_argument("--release", default="8.0")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--out", required=True)
    parser.add_argument("--compact", action="store_true",
                        help="convert an existing --out from float32 to "
                        "float16 vectors and rebuild the TURBO4 index")
    parser.add_argument("--add-cards", action="store_true",
                        help="add page cards and trees to an existing --out")
    return parser.parse_args()


# Man pages -----------------------------------------------------------------

def man_page_paths(root):
    """Yield (path, section) for the pages man(1) would find on amd64."""
    for tree in ("usr/share/man", "usr/X11R6/man"):
        base = os.path.join(root, tree)
        if not os.path.isdir(base):
            continue
        for folder in sorted(os.listdir(base)):
            if not folder.startswith("man"):
                continue
            section = folder[3:]
            section_dir = os.path.join(base, folder)
            for entry in sorted(os.listdir(section_dir)):
                path = os.path.join(section_dir, entry)
                if os.path.isfile(path):
                    yield path, section
                elif entry == ARCH:
                    for name in sorted(os.listdir(path)):
                        yield os.path.join(path, name), section


def read_man_pages(root, database):
    seen = {}
    for path, section in man_page_paths(root):
        text = render_man_page(path)
        if not text:
            continue
        digest = hashlib.sha256(text.encode()).hexdigest()
        name = re.sub(r"\.[0-9a-z]+$", "", os.path.basename(path))
        if digest in seen:          # hard link (MLINK) to a page we have
            continue
        seen[digest] = name
        sections = split_man_sections(text)
        names, title = parse_name_section(sections, name)
        tree = "x11" if "/X11R6/" in path else "base"
        database.execute(
            "INSERT OR IGNORE INTO pages (kind, name, section, title, aliases,"
            " tree) VALUES ('man', ?, ?, ?, ?, ?)",
            (name, section, title, " ".join(names), tree))
        label = f"{name}({section})"
        for heading, body in sections:
            for part in split_long(body):
                yield ("man", name, section, heading, title,
                       f"{label} {heading}\n{part}")


def render_man_page(path):
    result = subprocess.run(
        ["mandoc", "-T", "ascii", "-O", "width=100", path],
        capture_output=True, text=True, errors="replace")
    if result.returncode > 2 or not result.stdout:   # 2 = warnings only
        return ""
    # Strip overstrike (bold/underline) the way col -b does.
    return re.sub(r".\x08", "", result.stdout)


def split_man_sections(text):
    lines = text.splitlines()
    if lines and "(" in lines[0]:
        lines = lines[1:]       # header line: PF.CONF(5) File Formats ...
    if lines and re.match(r"^OpenBSD ", lines[-1].strip()):
        lines = lines[:-1]      # footer line: OpenBSD 8.0 date
    sections = []
    heading, body = "", []
    for line in lines:
        if SECTION_HEADING.match(line):
            if heading or any(l.strip() for l in body):
                sections.append((heading, dedent("\n".join(body))))
            heading, body = line.strip(), []
        else:
            body.append(line)
    sections.append((heading, dedent("\n".join(body))))
    return [(h, b) for h, b in sections if b.strip()]


def dedent(text):
    lines = text.strip("\n").splitlines()
    indents = [len(l) - len(l.lstrip()) for l in lines if l.strip()]
    cut = min(indents) if indents else 0
    return "\n".join(l[cut:] for l in lines).strip()


def parse_name_section(sections, fallback):
    for heading, body in sections:
        if heading == "NAME":
            joined = " ".join(body.split())
            if " - " in joined:
                names, title = joined.split(" - ", 1)
            else:
                names, title = joined, ""
            if len(title) > 160:     # a NAME section that runs on
                title = title[:160].rsplit(" ", 1)[0] + " ..."
            return [n.strip() for n in names.split(",") if n.strip()], title
    return [fallback], ""


def split_long(body):
    """Split a body at blank lines into parts no longer than MAX_CHUNK."""
    if len(body) <= MAX_CHUNK:
        yield body
        return
    part = ""
    for paragraph in re.split(r"\n\s*\n", body):
        while len(paragraph) > MAX_CHUNK:      # one huge paragraph
            if part:
                yield part
                part = ""
            yield paragraph[:MAX_CHUNK]
            paragraph = paragraph[MAX_CHUNK:]
        if part and len(part) + len(paragraph) + 2 > MAX_CHUNK:
            yield part
            part = ""
        part = f"{part}\n\n{paragraph}" if part else paragraph
    if part:
        yield part


# Packages ------------------------------------------------------------------

def read_packages(sqlports, index_path, database):
    built = set()
    with open(index_path) as index:
        for line in index:
            file_name = line.split()[-1]
            if file_name.endswith(".tgz"):
                built.add(file_name[:-4])
    ports = sqlite3.connect(sqlports)
    rows = ports.execute(
        "SELECT FULLPKGNAME, PKGSTEM, FullPkgPath, COMMENT, DESCR_CONTENTS,"
        " HOMEPAGE, CATEGORIES, README_CONTENTS, RUN_DEPENDS FROM PortsQ")
    for (full_name, stem, path, comment, description, homepage,
         categories, readme, run_depends) in rows:
        if full_name not in built:
            continue
        comment = comment or ""
        database.execute(
            "INSERT OR IGNORE INTO pages (kind, name, section, title, aliases,"
            " tree) VALUES ('pkg', ?, ?, ?, ?, 'pkg')",
            (stem or full_name, path, comment, full_name))
        summary = "\n".join(filter(None, [
            f"package {full_name} (pkg_add {stem or full_name})",
            f"port: {path}",
            f"comment: {comment}",
            f"categories: {categories}" if categories else "",
            f"homepage: {homepage}" if homepage else "",
            f"run depends: {run_depends}" if run_depends else "",
            "",
            (description or "").strip(),
        ]))
        yield ("pkg", stem or full_name, path, "DESCRIPTION", comment,
               summary)
        if readme:
            header = (f"pkg-readme for {full_name}, installed as "
                      f"/usr/local/share/doc/pkg-readmes/{stem}\n")
            for part in split_long(readme.strip()):
                yield ("pkg", stem or full_name, path, "PKG-README",
                       comment, header + part)


# Embedding -----------------------------------------------------------------

def insert_chunks(database, chunks):
    database.executemany(
        "INSERT INTO docs (kind, name, section, heading, title, body)"
        " VALUES (?, ?, ?, ?, ?, ?)", chunks)
    database.execute("INSERT INTO docs_fts(docs_fts) VALUES ('rebuild')")
    database.commit()


def embed_all(database, url, workers):
    rows = database.execute(
        "SELECT id, body FROM docs WHERE embedding IS NULL").fetchall()
    batches = [rows[i:i + 32] for i in range(0, len(rows), 32)]
    started = time.time()
    done = 0
    with ThreadPoolExecutor(workers) as pool:
        for batch, vectors in zip(batches, pool.map(
                lambda b: embed([DOCUMENT_PREFIX + t for _, t in b], url),
                batches)):
            database.executemany(
                "UPDATE docs SET embedding = ? WHERE id = ?",
                [(pack(v), row_id) for (row_id, _), v in zip(batch, vectors)])
            done += len(batch)
            if done % 2048 < 32:
                rate = done / (time.time() - started)
                print(f"embedded {done}/{len(rows)} ({rate:.0f}/s)",
                      file=sys.stderr)
    database.commit()


def embed(texts, url):
    request = urllib.request.Request(
        url + "/v1/embeddings",
        data=json.dumps({"input": texts}).encode(),
        headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=600) as response:
        data = json.load(response)["data"]
    return [normalize(item["embedding"])
            for item in sorted(data, key=lambda d: d["index"])]


def normalize(vector):
    norm = math.sqrt(sum(x * x for x in vector)) or 1.0
    return [x / norm for x in vector]


def pack(vector):
    return struct.pack(f"<{len(vector)}e", *vector)    # IEEE half floats


def compact(options):
    """Rewrite float32 vectors as float16 without embedding again."""
    database = sqlite3.connect(options.out)
    for table, key in (("docs", "id"), ("pages", "rowid")):
        rows = database.execute(
            f"SELECT {key}, embedding FROM {table}"
            f" WHERE length(embedding) = {DIMENSION * 4}").fetchall()
        database.executemany(
            f"UPDATE {table} SET embedding = ? WHERE {key} = ?",
            [(pack(struct.unpack(f"<{DIMENSION}f", blob)), row_id)
             for row_id, blob in rows])
        print(f"{table}: {len(rows)} vectors to float16", file=sys.stderr)
    database.commit()
    finish_index(database, options.vector, options)   # replaces INT8
    database.close()


def add_cards(options):
    """Upgrade a database built before page cards existed."""
    database = sqlite3.connect(options.out)
    columns = [r[1] for r in database.execute("PRAGMA table_info(pages)")]
    if "tree" not in columns:
        database.execute("ALTER TABLE pages ADD COLUMN tree TEXT NOT NULL"
                         " DEFAULT ''")
        database.execute("ALTER TABLE pages ADD COLUMN embedding BLOB")
    x11 = set()
    for path, section in man_page_paths(options.man):
        if "/X11R6/" in path:
            x11.add((re.sub(r"\.[0-9a-z]+$", "", os.path.basename(path)),
                     section))
    for (name, section) in x11:
        database.execute("UPDATE pages SET tree = 'x11' WHERE kind = 'man'"
                         " AND name = ? AND section = ?", (name, section))
    database.execute("UPDATE pages SET tree = 'base' WHERE kind = 'man'"
                     " AND tree = ''")
    # Packages were keyed by full name; key them by stem like docs.name.
    database.execute("UPDATE pages SET tree = 'pkg', aliases = name,"
                     " name = coalesce(nullif(aliases, ''), name)"
                     " WHERE kind = 'pkg' AND tree = ''")
    database.commit()
    embed_cards(database, options.embed_url, options.workers)
    finish_index(database, options.vector, options)
    database.close()


def card_text(kind, name, section, title, aliases):
    if kind == "pkg":
        return f"OpenBSD package {name}: {title}"
    also = [a for a in aliases.split() if a != name]
    text = f"{name}({section}): {title}"
    return text + (f" (also {', '.join(also[:8])})" if also else "")


def embed_cards(database, url, workers):
    """Embed one short card per page: the NAME line says what it is for."""
    rows = database.execute(
        "SELECT rowid, kind, name, section, title, aliases FROM pages"
        " WHERE embedding IS NULL").fetchall()
    batches = [rows[i:i + 32] for i in range(0, len(rows), 32)]
    with ThreadPoolExecutor(workers) as pool:
        for batch, vectors in zip(batches, pool.map(
                lambda b: embed([DOCUMENT_PREFIX + card_text(*r[1:])
                                 for r in b], url), batches)):
            database.executemany(
                "UPDATE pages SET embedding = ? WHERE rowid = ?",
                [(pack(v), r[0]) for r, v in zip(batch, vectors)])
    database.commit()
    print(f"embedded {len(rows)} page cards", file=sys.stderr)


# Finish --------------------------------------------------------------------

def finish_index(database, vector_path, options):
    """Build the TURBO4 scan index over the float16 vectors.

    kb/eval_quant.py measured, on 100 queries at k=40: TURBO4 alone finds
    94% of the exact top 40; TURBO4 for 80 candidates, re-ranked by exact
    cosine on the float16 vectors, finds 99.9% with the same top 10.
    """
    database.enable_load_extension(True)
    database.load_extension(vector_path)
    for table in ("docs", "pages"):
        database.execute(
            f"SELECT vector_init('{table}', 'embedding', 'type={VECTOR_TYPE},"
            f"dimension={DIMENSION},distance=COSINE,normalized=1')")
        count = database.execute(
            f"SELECT vector_quantize('{table}', 'embedding',"
            f" 'qtype={QUANTIZATION}')").fetchone()[0]
        print(f"quantized {count} {table} rows ({QUANTIZATION})",
              file=sys.stderr)
    database.executemany("INSERT OR REPLACE INTO meta VALUES (?, ?)", [
        ("openbsd_release", options.release),
        ("arch", ARCH),
        ("embed_model", options.embed_model),
        ("dimension", str(DIMENSION)),
        ("vector_type", VECTOR_TYPE),
        ("quantization", QUANTIZATION),
        ("candidates", str(CANDIDATES)),
        ("document_prefix", DOCUMENT_PREFIX),
        ("query_prefix", QUERY_PREFIX),
        ("built", time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())),
    ])
    database.commit()
    database.execute("VACUUM")


if __name__ == "__main__":
    main()
