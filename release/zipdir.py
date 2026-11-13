"""Zips a folder with Unix permissions kept (so the macOS app stays executable).
usage: python3 zipdir.py <src_dir> <out.zip>   (entries are relative to src_dir)"""
import os, stat, sys, time, zipfile

src, out = sys.argv[1], sys.argv[2]
stamp = time.localtime()[:6]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for dirpath, dirnames, filenames in os.walk(src):
        dirnames.sort()
        rel_dir = os.path.relpath(dirpath, src)
        if rel_dir != ".":
            info = zipfile.ZipInfo(rel_dir.replace(os.sep, "/") + "/", stamp)
            info.create_system = 3  # Unix, so the mode bits below are honoured
            info.external_attr = (stat.S_IFDIR | 0o755) << 16 | 0x10
            z.writestr(info, b"")
        for name in sorted(filenames):
            path = os.path.join(dirpath, name)
            mode = os.stat(path).st_mode
            info = zipfile.ZipInfo(os.path.relpath(path, src).replace(os.sep, "/"), stamp)
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | (0o755 if mode & 0o111 else 0o644)) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(path, "rb") as f:
                z.writestr(info, f.read())
