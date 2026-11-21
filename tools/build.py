"""Build tp once with traceable local metadata; optionally install that artifact."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from source_cache import source_identity, verified_artifact, record_artifact
from tool_history import locked_history

ROOT = Path(__file__).resolve().parents[1]


def install_artifact(binary, destination):
    """Replace the directory entry atomically so running executables retain their bytes."""
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".tp-install-", dir=destination.parent)
    os.close(descriptor)
    temporary = Path(temporary)
    try:
        shutil.copy2(binary, temporary)
        os.replace(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)

def component_versions(root):
    source = (root / "internal/version/version.go").read_text()
    values = dict(re.findall(r'^\s*(CLI|Server)\s*=\s*"((?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))"\s*$', source, re.M))
    if values.keys() != {"CLI", "Server"}:
        raise RuntimeError("Define the CLI and server semantic versions in internal/version/version.go")
    return values


def source_revision(root):
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root)
    status = subprocess.check_output(["git", "status", "--porcelain=v1", "--untracked-files=normal"], cwd=root)
    return hashlib.sha256(revision + b"\0" + status).hexdigest()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--install-only", action="store_true")
    parser.add_argument("--reuse", action="store_true", help="Reuse a verified binary with identical inputs and source identity")
    parser.add_argument("--debug", action="store_true", help="Retain Go debug symbols")
    args = parser.parse_args()
    binary = ROOT / "bin" / "tp"
    manifest = ROOT / ".cache/tools/build.json"
    with locked_history(manifest):
        build(args, binary, manifest)


def build(args, binary, manifest):
    if not args.install_only:
        subprocess.run([sys.executable, "-B", str(ROOT / "tools/pack_web.py")], cwd=ROOT, check=True)
        environment = dict(os.environ, CGO_ENABLED="0")
        identity = source_identity(ROOT, "build-v5-semver-debug" if args.debug else "build-v5-semver")
        identity = identity + source_revision(ROOT) if identity else None
        if args.reuse and verified_artifact(manifest, identity, binary):
            print("PASS: reused verified bin/tp (source, toolchain, target and flags unchanged)")
            return
        counter = ROOT / ".build-count"
        count = int(counter.read_text()) + 1 if counter.exists() else 1
        versions = component_versions(ROOT)
        binary.parent.mkdir(exist_ok=True)
        linker_flags = f"-X teleport/internal/version.BuildNumber={count}"
        if not args.debug:
            linker_flags = "-s -w " + linker_flags
        subprocess.run(["go", "build", "-trimpath", "-ldflags", linker_flags, "-o", str(binary), "./cmd/tp"], cwd=ROOT, env=environment, check=True)
        counter.write_text(str(count))
        record_artifact(manifest, identity, binary)
        print(f"Built {binary.relative_to(ROOT)} (CLI {versions['CLI']}, server {versions['Server']}, build {count})")
    else:
        env = subprocess.check_output(["go", "env", "GOBIN", "GOPATH"], text=True).splitlines()
        destination = Path(env[0] or str(Path(env[1].split(os.pathsep)[0]) / "bin"))
        destination.mkdir(parents=True, exist_ok=True)
        install_artifact(binary, destination / "tp")
        print(f"Installed {destination / 'tp'}")

if __name__ == "__main__":
    from tool_history import run
    run(main)
