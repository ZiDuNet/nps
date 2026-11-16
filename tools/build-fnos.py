#!/usr/bin/env python3
"""Build the native NPC fnOS FPK package.

The package contains no Web UI, no management listener and no server binary.
Installation is an introduction-only page; after install the fnOS desktop
opens the embedded Web UI, which is the only configuration surface and is
backed by real ``npc.conf`` files.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import hashlib


REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
DEFAULT_FNPACK_URL = "https://static2.fnnas.com/fnpack/fnpack-1.2.3-linux-amd64"

# fnOS exposes two platform families. The Go binary is architecture specific,
# so each platform gets its own FPK with the matching linux client binary.
ARCHES = {
    "amd64": {"goarch": "amd64", "platform": "x86"},
    "arm64": {"goarch": "arm64", "platform": "arm"},
}


def run(command: list[str], cwd: pathlib.Path | None = None,
        env: dict[str, str] | None = None) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, check=True, cwd=str(cwd or REPO_ROOT), env=env)


def source_version() -> str:
    version_file = REPO_ROOT / "lib" / "version" / "version.go"
    for line in version_file.read_text().splitlines():
        prefix = 'const VERSION = "'
        if line.startswith(prefix):
            return line[len(prefix):line.index('"', len(prefix))]
    raise SystemExit("无法从 lib/version/version.go 读取 VERSION")


def build_npc(output: pathlib.Path, goarch: str) -> None:
    environment = dict(os.environ)
    environment.update({"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": goarch})
    run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", str(output), "./cmd/npc"],
        cwd=REPO_ROOT, env=environment)


def copy_skeleton(staging: pathlib.Path) -> None:
    shutil.copytree(REPO_ROOT / "packaging" / "fnos", staging, dirs_exist_ok=True)
    for mode_dir in ("cmd",):
        for path in (staging / mode_dir).iterdir():
            if path.is_file() and not path.name.endswith(".md"):
                path.chmod(0o755)
    for path in staging.rglob(".DS_Store"):
        path.unlink()


def copy_template(staging: pathlib.Path) -> None:
    target = staging / "app" / "conf" / "npc.conf.example"
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(REPO_ROOT / "conf" / "npc.conf", target)
    target.chmod(0o644)


def make_icons(staging: pathlib.Path) -> None:
    run(["go", "run", "./tools/fnosicon", str(REPO_ROOT / "web" / "static" / "img" / "nps-logo.png"),
         str(staging), "64"])
    run(["go", "run", "./tools/fnosicon", str(REPO_ROOT / "web" / "static" / "img" / "nps-logo.png"),
         str(staging), "256"])
    ui_images = staging / "app" / "ui" / "images"
    ui_images.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(staging / "ICON.PNG", ui_images / "icon_64.png")
    shutil.copyfile(staging / "ICON_256.PNG", ui_images / "icon_256.png")


def generate_wizards(staging: pathlib.Path) -> None:
    """Use the trusted NPC binary itself, never a reimplementation."""
    with tempfile.TemporaryDirectory(prefix="nps-fnos-wizard-") as tmp:
        npc = pathlib.Path(tmp) / "npc"
        build_npc(npc, "amd64")
        output = pathlib.Path(tmp) / "install"
        config = pathlib.Path(tmp) / "config-root"
        run([str(npc), "-fnos=wizard-install", f"-fnos-config={config}", f"-fnos-wizard={output}"])
        data = json.loads(output.read_text(encoding="utf-8"))
        if not isinstance(data, list) or not data:
            raise SystemExit("生成的 install 向导为空")
        shutil.copyfile(output, staging / "wizard" / "install")
        (staging / "wizard" / "install").chmod(0o644)


def write_manifest(staging: pathlib.Path, version: str, platform: str) -> None:
    lines = []
    for line in (REPO_ROOT / "packaging" / "fnos" / "manifest").read_text().splitlines():
        key, _, value = line.partition("=")
        if key.strip() == "version":
            value = version
        elif key.strip() == "platform":
            value = platform
        lines.append(f"{key}={value}")
    (staging / "manifest").write_text("\n".join(lines) + "\n")


def place_binary(staging: pathlib.Path, npc: pathlib.Path) -> None:
    target = staging / "app" / "bin" / "npc"
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(npc, target)
    target.chmod(0o755)


def build_one(arch: str, output_dir: pathlib.Path, version: str, fnpack: pathlib.Path,
              npc: pathlib.Path | None, keep: pathlib.Path | None) -> pathlib.Path:
    info = ARCHES[arch]
    staging = (keep or pathlib.Path(tempfile.mkdtemp(prefix="nps-fnos-build-"))) / f"npc-pro-{arch}"
    staging.mkdir(parents=True, exist_ok=True)
    copy_skeleton(staging)
    copy_template(staging)
    make_icons(staging)
    generate_wizards(staging)
    write_manifest(staging, version, info["platform"])
    if npc is None:
        npc = staging.parent / f"npc-{arch}"
        build_npc(npc, info["goarch"])
    place_binary(staging, npc)
    # fnpack writes the .fpk into the process working directory, so keep it
    # inside the dedicated build folder instead of polluting the source tree.
    run([str(fnpack), "build", "-d", str(staging)], cwd=staging.parent)
    produced = list(staging.parent.glob("*.fpk"))
    if len(produced) != 1:
        raise SystemExit(f"fnpack 未生成唯一 FPK：{produced}")
    result = output_dir / f"npc-pro-{version}-{arch}.fpk"
    shutil.move(str(produced[0]), result)
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=sorted(ARCHES), action="append", required=True,
                        help="目标架构，可重复传入")
    parser.add_argument("--output", type=pathlib.Path, default=REPO_ROOT / "dist" / "fnos")
    parser.add_argument("--npc", type=pathlib.Path, action="append", default=[],
                        help="已构建的 linux npc 二进制，可重复传入；按 --arch 顺序或文件名匹配")
    parser.add_argument("--fnpack", type=pathlib.Path,
                        default=pathlib.Path.home() / ".cache" / "nps-fnos-research.lRfM3X" / "fnpack-1.2.3-linux-amd64")
    parser.add_argument("--keep", type=pathlib.Path, help="保留构建暂存目录")
    args = parser.parse_args()
    args.fnpack = args.fnpack.resolve()
    args.output = args.output.resolve()
    args.npc = [path.resolve() for path in args.npc]
    if args.keep is not None:
        args.keep = args.keep.resolve()
    version = source_version()
    args.output.mkdir(parents=True, exist_ok=True)
    if not args.fnpack.exists():
        raise SystemExit(f"找不到 fnpack：{args.fnpack}；请先从 {DEFAULT_FNPACK_URL} 下载")
    provided: dict[str, pathlib.Path] = {}
    for path in args.npc:
        for arch in ARCHES:
            if path.name.endswith(arch):
                provided[arch] = path
                break
        else:
            raise SystemExit(f"无法识别 {path} 的目标架构；文件名需包含 amd64 或 arm64")
    for arch in args.arch:
        npc = provided.get(arch)
        if npc is not None and not npc.exists():
            raise SystemExit(f"{arch} 的 npc 不存在：{npc}")
        result = build_one(arch, args.output, version, args.fnpack, npc, args.keep)
        digest = hashlib.sha256(result.read_bytes()).hexdigest()
        (args.output / f"{result.name}.sha256").write_text(f"{digest}  {result.name}\n")
        print(f"已生成 {result}")


if __name__ == "__main__":
    main()
