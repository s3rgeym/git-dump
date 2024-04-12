#!/usr/bin/env python3
# pylint: disable=C,R,W
"""\
null-dependency git dumper
"""
import argparse
import codecs
import functools
import io
import math
import multiprocessing
import os
import pathlib
import re
import shutil
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import traceback
import zlib
from collections import namedtuple
from multiprocessing import JoinableQueue, Manager, Process
from urllib.error import HTTPError
from urllib.parse import unquote, urljoin, urlsplit
from urllib.request import HTTPHandler, HTTPSHandler, Request, build_opener

__version__ = "0.1.0"
__author__ = "Sergey M"

GIT_DIR = "/.git/"
GIT_COMMIT_TYPE = "commit"
GIT_TREE_TYPE = "tree"

GIT_COMMON_FILES = [
    "COMMIT_EDITMSG",
    "config",
    "description",
    "FETCH_HEAD",
    "HEAD",  # ref: refs/heads/main
    # hooks
    "hooks/applypatch-msg",
    "hooks/commit-msg",
    "hooks/fsmonitor-watchman",
    "hooks/post-update",
    "hooks/pre-applypatch",
    "hooks/pre-commit",
    "hooks/pre-merge-commit",
    "hooks/prepare-commit-msg",
    "hooks/pre-push",
    "hooks/pre-rebase",
    "hooks/pre-receive",
    "hooks/push-to-checkout",
    "hooks/sendemail-validate",
    "hooks/update",
    # info
    "info/refs",  # содержит хеши
    "info/exclude",
    # logs
    # содержат хеши
    "logs/HEAD",
    "logs/refs/heads/main",
    "logs/refs/heads/master",
    "logs/refs/heads/develop",
    "logs/refs/remotes/origin/main",
    "logs/refs/remotes/origin/master",
    "logs/refs/remotes/origin/develop",
    # objects
    "objects/info/packs",  # P pack-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX.pack
    "ORIG_HEAD",
    "packed-refs",  # содержит хеши
    # refs
    # содержат хеши
    "refs/heads/main",
    "refs/heads/master",
    "refs/heads/develop",
    "refs/remotes/origin/main",
    "refs/remotes/origin/master",
    "refs/remotes/origin/develop",
]

USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"

REFERER = "https://www.google.com/"

SHA1_RE = re.compile(r"\b[a-f\d]{40}\b")
PACK_RE = re.compile(r"\bpack-" + SHA1_RE.pattern[2:-2] + r"\b")

MAX_PARSE_SIZE = 1 << 27  # 128 MiB

print = functools.partial(print, file=sys.stderr)

ASSET_EXTS = (
    ".css",
    ".html",
    ".htm",
    ".min.js",
    ".css",
    ".map",
    ".jpeg",
    ".jpg",
    ".png",
    ".gif",
    ".bmp",
    ".ico",
    ".eot",
    ".ttf",
    ".ttf2",
    ".otf",
    ".psd",
    ".doc",
    ".docx",
    ".pdf",
    ".mp3",
    ".mp4",
    ".mov",
    ".webm",
    ".ai",
    ".swf",
    ".apk",
)


class ArgumentFormatter(
    argparse.ArgumentDefaultsHelpFormatter,
    argparse.RawDescriptionHelpFormatter,
):
    pass


def parse_args(argv):
    parser = argparse.ArgumentParser(formatter_class=ArgumentFormatter)
    parser.add_argument(
        "-u",
        "--url",
        help="git url",
        nargs="*",
        dest="urls",
        default=[],
    )
    parser.add_argument(
        "-i",
        "--input",
        help="input file with list of urls",
        type=argparse.FileType(),
        default="-",
    )
    parser.add_argument(
        "-o",
        "--output",
        dest="output_dir",
        help="output directory",
        default=pathlib.Path("dump"),
        type=pathlib.Path,
    )
    parser.add_argument(
        "-t",
        "--timeout",
        help="fetch timeout",
        type=float,
        default=15.0,
    )
    parser.add_argument(
        "-A", "--user-agent", help="user-agent", default=USER_AGENT
    )
    parser.add_argument(
        "-w",
        "--workers",
        dest="num_workers",
        help="number of workers",
        type=int,
        default=max(4, multiprocessing.cpu_count() * 2 - 1),
    )
    # parser.add_argument(
    #     "-k",
    #     "--keep-files",
    #     "--keep",
    #     help="skip downloading existings files",
    #     action="store_true",
    # )
    parser.add_argument(
        "--download-assets",
        help="force download assets",
        action="store_true",
        default=False,
    )
    args = parser.parse_args(argv)
    return args


def normalize_git_url(url):
    url, *_ = url.split("#")
    url = url.split("?")[0]
    return (
        ["http://", ""]["://" in url]
        + (url + "/").split(GIT_DIR)[0].rstrip("/")
        + GIT_DIR
    )


class GitIndex:
    HEADER_STRUCT = struct.Struct("!4s2I")

    Entry = namedtuple("Entry", ["sha1", "filename"])

    signature = None
    version = None
    num_entries = None

    def __init__(self, fp):
        self.fp = fp
        self.parse_header()

    def parse_header(self):
        (
            self.signature,
            self.version,
            self.num_entries,
        ) = self.HEADER_STRUCT.unpack(self.fp.read(self.HEADER_STRUCT.size))
        assert self.signature == b"DIRC"
        assert self.version in (2, 3, 4)
        assert self.num_entries >= 0

    def read_null_string(self):
        """read null-terminated string"""
        res = b""
        while True:
            ch = self.fp.read(1)
            if ch == b"\0":
                break
            assert ch
            res += ch
        return res

    def get_entries(self):
        n = self.num_entries
        while n > 0:
            self.fp.seek(40, io.SEEK_CUR)
            # 20 - байт хеш, 2 - флаги
            # TODO: имя файла может храниться в флагах
            hash = codecs.encode(self.fp.read(22)[:-2], "hex").decode()
            yield self.Entry(
                sha1=hash,
                filename=self.read_null_string().decode(errors="replace"),
            )
            # Размер entry кратен 8 и добивается NULL-байтами
            size = self.fp.tell() - self.HEADER_STRUCT.size
            self.fp.seek(math.ceil(size / 8) * 8 - size, io.SEEK_CUR)
            # TODO: обработка extensions
            n -= 1


# https://cdn.ttgtmedia.com/rms/onlineImages/storage-memory_capacity_chart.png
def humansize(n, base=1024, units="BKMGTPEZY"):
    """
    >>> humansize(1024)
    '1 K'
    >>> humansize(1025)
    '1.01 K'
    >>> humansize(1500000)
    '1.44 M'
    >>> humansize(1234567890)
    '1.15 G'
    """
    if n == 0:
        return "0"
    exp = min(math.floor(math.log(n) / math.log(base)), len(units) - 1)
    return "{:.2f} {}".format(
        math.ceil(n / base**exp * 100) / 100, units[exp]
    ).replace(".00", "")


class DecodeError(Exception):
    pass


class GitDumper(Process):
    def __init__(
        self,
        queue,
        seen,
        output_path,
        timeout,
        user_agent,
        force_download_assets,
    ):
        super().__init__()
        self.queue = queue
        self.seen = seen
        self.output_path = output_path
        self.timeout = timeout
        self.user_agent = user_agent
        self.force_download_assets = force_download_assets
        self.start()

    @property
    @functools.lru_cache()
    def opener(self):
        return build_opener(
            HTTPHandler(),
            HTTPSHandler(context=ssl._create_unverified_context()),
        )

    def http_download(self, url, fp):
        req = Request(
            url,
            headers={
                "User-Agent": self.user_agent,
                "Referer": REFERER,
                "Accept": "*/*",
            },
        )
        with self.opener.open(req, timeout=self.timeout) as resp:
            shutil.copyfileobj(resp, fp)
            try:
                return fp.tell()
            finally:
                fp.seek(0)

    def get_object_url(self, git_url, hash):
        return urljoin(
            git_url + ["/", ""][git_url.endswith("/")],
            "objects/{}/{}".format(hash[:2], hash[2:]),
        )

    def extract_hashes(self, content, git_base_url):
        for hash in SHA1_RE.findall(content):
            self.queue.put(
                self.get_object_url(git_base_url, hash),
            )

    def parse_obj_file(self, fp, git_base_url):
        # Tree и Commit содержат хеши
        # https://www.geeksforgeeks.org/git-object-model/
        # https://git-scm.com/book/ru/v2/Git-изнутри-Объекты-Git
        try:
            decompressed = zlib.decompress(fp.read())
        except zlib.error as e:
            raise DecodeError() from e
        pos = decompressed.find(b"\x00")
        obj_type = decompressed[:pos].decode()
        if obj_type in [GIT_COMMIT_TYPE, GIT_TREE_TYPE]:
            self.extract_hashes(
                decompressed.decode(errors="ignore"), git_base_url
            )

    def parse_git(self, temp_file, url):
        pos = url.find(GIT_DIR) + len(GIT_DIR)
        git_base_url, path = url[:pos], url[pos:]
        if path == "index":
            for entry in GitIndex(temp_file).get_entries():
                object_url = self.get_object_url(git_base_url, entry.sha1)

                print(
                    "\033[36mFound {} => {}\033[0m".format(
                        object_url, entry.filename
                    )
                )

                self.queue.put(object_url)

                lower_filename = entry.filename.lower()

                # объектный файл может отдаваться как text/html в UTF-8, поэтому из него не получится восстановить данные
                if lower_filename.endswith(".php"):
                    continue

                if not self.force_download_assets and lower_filename.endswith(
                    ASSET_EXTS
                ):
                    continue

                file_url = (
                    git_base_url[: -len(GIT_DIR)].rstrip("/")
                    + "/"
                    + entry.filename.lstrip("/").replace(" ", "%20")
                )

                self.queue.put(file_url)
            # Если index валиден, то выкачиваем остальные файлы
            for f in GIT_COMMON_FILES:
                self.queue.put(urljoin(git_base_url, f))
            return
        if re.match("objects/[a-f0-9]{2}/[a-f0-9]{38}$", path):
            self.parse_obj_file(temp_file, git_base_url)
            return
        # Если верить док-ии, то в паках...
        # https://git-scm.com/book/ru/v2/Git-изнутри-Pack-файлы
        if "pack-" in path:
            # TODO: parse packs
            return
        content = temp_file.read().decode(errors="ignore")
        # assert '<html' not in content.lower()
        if path == "objects/info/packs":
            for pack in PACK_RE.findall(content):
                for ext in ("idx", "pack"):
                    self.queue.put(
                        urljoin(
                            git_base_url,
                            "objects/pack/{}.{}".format(pack, ext),
                        )
                    )
            return
        self.extract_hashes(content, git_base_url)

    def run(self):
        while 42:
            try:
                url = self.queue.get()
                if url is None:
                    break
                # assert GIT_DIR in url
                if url in self.seen:
                    # print("\033[31mSeen: {}\033[0m".format(url))
                    continue
                # файл может долго скачиваться, поэтому все же произойдет
                # обработка одного и того же файла разными процессами
                self.seen[url] = 1
                # Сохраняем все во временный файл, который будет удален в случае ошибки
                with tempfile.NamedTemporaryFile(delete=False) as temp_file:
                    try:
                        size = self.http_download(url, temp_file)
                        if GIT_DIR in url:
                            if size <= MAX_PARSE_SIZE:
                                try:
                                    self.parse_git(temp_file, url)
                                except DecodeError:
                                    print(
                                        "\033[31mNon zlib-deflated data: {}\033[0m".format(
                                            url
                                        )
                                    )
                                    continue
                            else:
                                print(
                                    "\033[31mWARN: Too Large to Parse ({} > {}) - {}\033[0m".format(
                                        humansize(size),
                                        humansize(MAX_PARSE_SIZE),
                                        url,
                                    )
                                )
                        sp = urlsplit(url)

                        # hostname = sp.netloc.split(":")[0].strip("[]")

                        dest = (
                            self.output_path
                            / sp.netloc
                            / unquote(sp.path.lstrip("/"))
                        )

                        try:
                            dest.parent.mkdir(parents=True)
                        except FileExistsError:
                            pass

                        temp_file.close()
                        # Эта функция работает только в пределах одной файловой системы, те из tmpfs
                        # файл нельзя переместить куда-то в /home
                        # os.replace(temp_file.name, str(dest))
                        shutil.move(temp_file.name, str(dest))
                        print("\033[32mSaved: {}\033[0m".format(dest))
                    except HTTPError as ex:
                        print(
                            "\033[31mError {}: {}\033[0m".format(
                                ex.status,
                                url,
                            )
                        )
                    except socket.timeout:
                        print("\033[31mTimeout: {}\033[0m".format(url))
                    finally:
                        if os.path.exists(temp_file.name):
                            os.unlink(temp_file.name)
            except Exception as ex:
                print(
                    "\033[31mAn Unexpected Error has occurred:",
                    "",
                    "".join(
                        traceback.format_exception(
                            type(ex), ex, ex.__traceback__
                        )
                    ),
                    "\033[0m",
                    sep=os.linesep,
                )
            finally:
                self.queue.task_done()


def git_checkout_files(hosts, output_dir):
    cwd = os.path.curdir
    for host in hosts:
        try:
            repo_path = output_dir / host
            if not repo_path.exists():
                continue
            os.chdir(str(repo_path))
            subprocess.check_call(["git", "checkout", "--", "."])
            print("\033[32mRestored: {}\033[0m".format(repo_path))
        except subprocess.CalledProcessError:
            print("\033[31mCan't restore: {}\033[0m".format(repo_path))
        finally:
            os.chdir(cwd)


def main(argv=None):
    args = parse_args(argv)

    urls = args.urls.copy()

    if not args.input.isatty():
        urls.extend(filter(None, map(str.strip, args.input)))

    urls = set(map(normalize_git_url, urls))
    # assert urls

    queue = JoinableQueue()
    manager = Manager()
    seen = manager.dict()

    for url in urls:
        queue.put_nowait(urljoin(url, "index"))

    workers = [
        GitDumper(
            queue=queue,
            seen=seen,
            output_path=args.output_dir,
            timeout=args.timeout,
            user_agent=args.user_agent,
            force_download_assets=args.download_assets,
        )
        for _ in range(args.num_workers)
    ]

    queue.join()

    for _ in range(len(workers)):
        queue.put(None)

    for w in workers:
        w.join()

    print("\033[33mDump tasks completed!\033[0m")

    print("\033[33mGit checkout files\033[0m")
    hosts = set(urlsplit(u).netloc for u in seen.keys())
    git_checkout_files(hosts, args.output_dir)
    print("\033[33mFinished!\033[0m")


if __name__ == "__main__":
    sys.exit(main())
