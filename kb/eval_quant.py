#!/usr/bin/env python3
"""Compare sqlite-vector quantized scans against the exact full scan.

For each quantization type, rebuilds the quantized index on a copy of the
knowledge base and reports, for the docs and pages tables, the share of
the exact top-k that the quantized scan finds (recall@k) at the depths
puffy uses, plus scan time.

    python3 kb/eval_quant.py --db data/puffy-kb.db \
        --vector build/sqlite-vector/dist/vector --work /tmp/kb-eval.db
"""

import argparse
import json
import random
import shutil
import sqlite3
import struct
import time
import urllib.request

QUERIES = [
    "forward port 80 to an internal web server",
    "start a service at boot",
    "give a user root privileges for some commands",
    "install security patches for the base system",
    "configure a static ip address",
    "set up a wireguard tunnel",
    "install a postgres database server",
    "copy a string safely in C",
    "rotate log files",
    "restrict a program's system calls",
    "schedule a nightly backup job",
    "set up a dns resolver with caching",
    "serve a static website over https",
    "get a free TLS certificate from let's encrypt",
    "add a new user account with a home directory",
    "block brute force ssh login attempts",
    "mount a usb stick",
    "encrypt a disk with softraid",
    "upgrade to the next OpenBSD release",
    "update installed packages",
    "run a virtual machine",
    "send mail from a server",
    "keep the clock in sync",
    "configure wifi with wpa2",
    "see which process listens on a port",
    "limit bandwidth for a host",
    "create a bridge between two interfaces",
    "parse command line options in C",
    "read a file line by line in C",
    "allocate memory and check for overflow",
    "write a daemon that drops privileges",
    "restrict filesystem access of a process",
    "show kernel tunables for networking",
    "load the firewall rules without enabling",
    "configure a load balancer for web servers",
    "change the default shell of a user",
    "check disk usage of directories",
    "build software from the ports tree",
    "python web framework package",
    "text editor with syntax highlighting",
]


def main():
    options = parse_arguments()
    shutil.copyfile(options.db, options.work)
    database = sqlite3.connect(options.work)
    database.enable_load_extension(True)
    database.load_extension(options.vector)
    meta = dict(database.execute("SELECT key, value FROM meta"))
    dimension = int(meta["dimension"])
    for table in ("docs", "pages"):
        database.execute(
            f"SELECT vector_init('{table}', 'embedding',"
            f" 'type=FLOAT32,dimension={dimension},distance=COSINE,"
            "normalized=1')")

    queries = QUERIES + sample_titles(database, 60)
    vectors = [pack(v) for v in embed(
        [meta["query_prefix"] + q for q in queries], options.embed_url)]
    print(f"{len(queries)} queries, k={options.k}\n")

    exact = {}
    for table in ("docs", "pages"):
        started = time.time()
        exact[table] = [scan(database, "vector_full_scan", table, v,
                             options.k) for v in vectors]
        exact[table + "_ms"] = (time.time() - started) * 1000 / len(vectors)

    print(f"{'qtype':8} {'table':6} {'recall@k':>9} {'recall@10':>10}"
          f" {'top1':>6} {'ms/query':>9} {'index MB':>9}")
    for table in ("docs", "pages"):
        print(f"{'float32':8} {table:6} {1:9.3f} {1:10.3f} {1:6.2f}"
              f" {exact[table + '_ms']:9.1f} {'-':>9}")
    for qtype in options.qtypes.split(","):
        for table in ("docs", "pages"):
            database.execute(
                f"SELECT vector_quantize('{table}', 'embedding',"
                f" 'qtype={qtype}')").fetchone()
            database.execute(
                f"SELECT vector_quantize_preload('{table}', 'embedding')")
            size = database.execute(
                f"SELECT vector_quantize_memory('{table}', 'embedding')"
            ).fetchone()[0] / 1048576
            started = time.time()
            found = [scan(database, "vector_quantize_scan", table, v,
                          options.k) for v in vectors]
            ms = (time.time() - started) * 1000 / len(vectors)
            recall_k, recall_10, top1 = compare(exact[table], found)
            print(f"{qtype:8} {table:6} {recall_k:9.3f} {recall_10:10.3f}"
                  f" {top1:6.2f} {ms:9.1f} {size:9.1f}")
            for depth in filter(None, options.oversample.split(",")):
                depth = int(depth)
                started = time.time()
                reranked = [rerank(database, table, v, scan(
                    database, "vector_quantize_scan", table, v, depth),
                    options.k) for v in vectors]
                ms = (time.time() - started) * 1000 / len(vectors)
                recall_k, recall_10, top1 = compare(exact[table], reranked)
                label = f"+{depth}f16"
                print(f"{label:8} {table:6} {recall_k:9.3f}"
                      f" {recall_10:10.3f} {top1:6.2f} {ms:9.1f}")


def parse_arguments():
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", required=True)
    parser.add_argument("--vector", required=True)
    parser.add_argument("--work", required=True,
                        help="scratch copy; quantization rewrites it")
    parser.add_argument("--embed-url", default="http://127.0.0.1:8089")
    parser.add_argument("--qtypes", default="INT8,TURBO4,TURBO3,TURBO2")
    parser.add_argument("-k", type=int, default=40)
    parser.add_argument("--oversample", default="",
                        help="comma list of candidate depths: report "
                        "recall after re-ranking N candidates by float16")
    return parser.parse_args()


def sample_titles(database, count):
    """Page titles read like the short questions people ask."""
    rows = database.execute(
        "SELECT title FROM pages WHERE length(title) BETWEEN 15 AND 90"
        " AND (kind = 'pkg' OR section IN ('1', '5', '8'))"
        " ORDER BY name").fetchall()
    random.seed(80)
    return [r[0] for r in random.sample(rows, count)]


def scan(database, function, table, vector, k):
    return [r[0] for r in database.execute(
        f"SELECT rowid FROM {function}('{table}', 'embedding', ?, ?)",
        (vector, k))]


def rerank(database, table, query, candidates, k):
    """Exact cosine on float16 copies of the candidates (unit vectors)."""
    q = struct.unpack(f"<{len(query) // 4}f", query)
    marks = ",".join("?" * len(candidates))
    rows = database.execute(
        f"SELECT rowid, embedding FROM {table} WHERE rowid IN ({marks})",
        candidates).fetchall()
    scored = []
    for rowid, blob in rows:
        v = struct.unpack(f"<{len(blob) // 4}f", blob)
        half = struct.unpack(f"<{len(v)}e", struct.pack(f"<{len(v)}e", *v))
        scored.append((-sum(a * b for a, b in zip(q, half)), rowid))
    return [rowid for _, rowid in sorted(scored)[:k]]


def compare(exact, found):
    recall_k = recall_10 = top1 = 0.0
    for truth, got in zip(exact, found):
        recall_k += len(set(truth) & set(got)) / max(len(truth), 1)
        recall_10 += len(set(truth[:10]) & set(got[:10])) / 10
        top1 += 1.0 if truth[:1] == got[:1] else 0.0
    n = len(exact)
    return recall_k / n, recall_10 / n, top1 / n


def embed(texts, url):
    vectors = []
    for start in range(0, len(texts), 32):
        request = urllib.request.Request(
            url + "/v1/embeddings",
            data=json.dumps({"input": texts[start:start + 32]}).encode(),
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=600) as response:
            data = json.load(response)["data"]
        for item in sorted(data, key=lambda d: d["index"]):
            norm = sum(x * x for x in item["embedding"]) ** 0.5 or 1.0
            vectors.append([x / norm for x in item["embedding"]])
    return vectors


def pack(vector):
    return struct.pack(f"<{len(vector)}f", *vector)


if __name__ == "__main__":
    main()
