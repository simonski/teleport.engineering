# Style
- Never use first-person pronouns (I, me, my, we, us, our). Speak in the third person or use an objective, passive, or imperative voice.

# Git
- preserve linear history in git
- do not use worktrees
- use feature branches

# Automation
- keep build, test, and deployment automation inside the local project harness; do not add GitHub Actions or other external automation for now

# Tooling
- When writing tools, do not create adhoc perl, ruby, python scripts in-line, rather create the tool under ./tools and try to reuse
- Avoid anything other than go, or python for adhoc tooling, avoid curl where possible
- Retain the tool invocation duration and outcomes of tool calls in an tools-calls.csv file so we can look at the performance of the e2e build and tooling over phases and use that to improve the agentic engineering loop

# Downloading
- do not download software without express permission (E.g. zig, nim, docker)

# Versions
- Maintain separate semantic application versions for the CLI client and server in `internal/version/version.go`.   Do not use uuids, or dirty.  Vanilla N.N.N semantics.
- Increment the affected component's minor version and reset its patch to zero when adding a command or server API command; increment both when both components change. A release must not reuse the same component version for different command capabilities. Follow the existing protocol version policy independently.
- Every server-backed CLI invocation checks the server's advertised application version. If client and server major/minor versions differ, warn on stderr with both versions and identify the older component to upgrade. Patch-only differences do not trigger a warning. Preserve machine-readable JSON on stdout. If a version cannot be retrieved, do not invent one; an unavailable command must still report the unavailable server version.

# Building
- use a Makefile where empty pretty prints the targets one per line, jsutified output, use make clean build install and run to run locally.  -p N is the port number if we are running a server
- the CLI outputs human-readable pretty printed output, not json.  JSON when printed is pretty and only when -json is passed on the CLI

# CLI tool
- use single hyphen options, not double in the CLI/binary application

# SKILL
- use the CLI binary to call the server - do not use curl, pythone etc
