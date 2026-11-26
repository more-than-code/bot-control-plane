#!/usr/bin/env bash
# cloud-setup.sh: prepare a Claude cloud container (Ubuntu, run as root) for
# the Deploybot test suites.
#
# It installs PostgreSQL 18 (the PGDG apt repo) with TimescaleDB (Timescale's
# packagecloud apt repo), both signing keys checked by fingerprint, and Node 24
# (with pnpm, tarball sha256 checked), creates the non-root user
# deploybot-test, starts PostgreSQL 18 on port 5433 (the image's own cluster
# keeps 5432) with UTF8 encoding, and creates the test database
# deploybot_test with the roles from api/migrations/roles.sql.example, as
# api/README.md describes. The two test roles log in without a password, from
# 127.0.0.1 only, to deploybot_test only (pg_hba trust).
#
# Safe to rerun: each step checks first and says what it did. PostgreSQL does
# not survive a container resume; a rerun starts it again in seconds. No secrets are used or
# printed. Command output goes to $LOG; on failure its last lines are shown.
#
# Usage, as root:  bash scripts/cloud-setup.sh
#
# Network: apt.postgresql.org, packagecloud.io and the host packagecloud
# redirects every download to (d3fo0g5hm7lbuv.cloudfront.net) must be allowed,
# besides nodejs.org and the package managers (apt, npm, the Go proxy).

set -euo pipefail

TEST_USER=deploybot-test
TEST_DB=deploybot_test
NODE_VERSION=24.22.3
NODE_SHA256=4f2b175a6d53a5c617a80dba2676238a0688cef28c6da4eba35c7d844e66fcfb # node-v24.22.3-linux-x64.tar.xz, nodejs.org SHASUMS256.txt
NODE_DIR=/opt/node24
PNPM_VERSION=10.28.0
PG_VER=18
PG_CLUSTER=main
PG_PORT=5433
PGDG_KEY_URL=https://apt.postgresql.org/pub/repos/apt/ACCC4CF8.asc
PGDG_KEY_FPR=B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8 # PostgreSQL Debian Repository
TS_KEY_URL=https://packagecloud.io/timescale/timescaledb/gpgkey
TS_KEY_FPR=1005FB68604CE9B8F6879CF759F18EDF47F24417 # packagecloud.io/timescale/timescaledb
HBA_MARK='# deploybot cloud-setup: test roles, local only'
LOG=/tmp/deploybot-cloud-setup.log

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ROLES_SQL=$REPO/api/migrations/roles.sql.example

say() { printf 'cloud-setup: %s\n' "$*"; }
die() { say "error: $*" >&2; exit 1; }
run() {
	if ! "$@" >>"$LOG" 2>&1; then
		say "failed: $*" >&2
		tail -n 20 "$LOG" >&2
		exit 1
	fi
}
as_pg() { runuser -u postgres -- "$@"; }
psql_pg() { as_pg psql -X -q -v ON_ERROR_STOP=1 -p "$PG_PORT" "$@"; }

[ "$(id -u)" -eq 0 ] || die "run as root"
[ -f "$ROLES_SQL" ] || die "missing $ROLES_SQL"
: >"$LOG"
export DEBIAN_FRONTEND=noninteractive

# add_apt_repo NAME KEY_URL FINGERPRINT URL SUITE: adds a signed apt source
# once; the key must have exactly the given primary key fingerprint.
apt_changed=0
add_apt_repo() {
	local name=$1 key_url=$2 fpr=$3 url=$4 suite=$5
	local keyring=/usr/share/keyrings/$name.gpg list=/etc/apt/sources.list.d/$name.list tmp got want
	if [ ! -s "$keyring" ]; then
		tmp=$(mktemp -d)
		run curl -fsSL --retry 3 -o "$tmp/key.asc" "$key_url"
		got=$(gpg --show-keys --with-colons "$tmp/key.asc" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')
		[ "$got" = "$fpr" ] || die "$name key fingerprint is '$got', expected $fpr"
		run gpg --batch --yes --dearmor -o "$keyring" "$tmp/key.asc"
		rm -rf "$tmp"
		say "added $name apt key $fpr"
	fi
	want="deb [signed-by=$keyring] $url $suite main"
	if [ "$(cat "$list" 2>/dev/null)" != "$want" ]; then
		printf '%s\n' "$want" >"$list"
		apt_changed=1
		say "added apt source $list"
	fi
}
installed() { dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'; }

. /etc/os-release
TS_PKG=timescaledb-2-postgresql-$PG_VER
if installed "postgresql-$PG_VER" && installed "$TS_PKG"; then
	say "postgresql-$PG_VER and $TS_PKG already installed"
else
	add_apt_repo pgdg "$PGDG_KEY_URL" "$PGDG_KEY_FPR" https://apt.postgresql.org/pub/repos/apt "$VERSION_CODENAME-pgdg"
	add_apt_repo timescaledb "$TS_KEY_URL" "$TS_KEY_FPR" https://packagecloud.io/timescale/timescaledb/ubuntu/ "$VERSION_CODENAME"
	run apt-get update
	if installed "postgresql-$PG_VER"; then
		say "postgresql-$PG_VER already installed"
	else
		# Create the cluster below, on a fixed port, not from the package.
		mkdir -p /etc/postgresql-common/createcluster.d
		printf 'create_main_cluster = false\n' >/etc/postgresql-common/createcluster.d/deploybot-cloud-setup.conf
		run apt-get install -y --no-install-recommends "postgresql-$PG_VER"
		say "installed postgresql-$PG_VER $(dpkg-query -W -f='${Version}' "postgresql-$PG_VER")"
	fi
	# Take the newest TimescaleDB build whose postgresql-N dependency the
	# installed server meets (a new build can need a newer minor first).
	pg_have=$(dpkg-query -W -f='${Version}' "postgresql-$PG_VER")
	ts_ver=
	while read -r v need; do
		if [ -z "$need" ] || dpkg --compare-versions "$pg_have" ge "$need"; then
			ts_ver=$v
			break
		fi
	done < <(apt-cache show "$TS_PKG" | awk -v p="postgresql-$PG_VER" '
		/^Version:/ { v = $2; need = "" }
		/^Depends:/ { n = split(substr($0, 10), d, /, */)
			for (i = 1; i <= n; i++) if (d[i] ~ "^" p " \\(>= ") { need = d[i]; sub(/.*>= /, "", need); sub(/\).*/, "", need) } }
		/^$/ && v != "" { print v, need; v = "" }
		END { if (v != "") print v, need }' | sort -V -r -k1,1)
	[ -n "$ts_ver" ] || die "no $TS_PKG build works with postgresql-$PG_VER $pg_have"
	run apt-get install -y --no-install-recommends "$TS_PKG=$ts_ver" "timescaledb-2-loader-postgresql-$PG_VER=$ts_ver"
	say "installed $TS_PKG $ts_ver"
fi

# --- PostgreSQL 18 cluster --------------------------------------------------
PG_ETC=/etc/postgresql/$PG_VER/$PG_CLUSTER
if [ -f "$PG_ETC/postgresql.conf" ]; then
	have_port=$(pg_lsclusters --no-header | awk -v v="$PG_VER" -v c="$PG_CLUSTER" '$1==v && $2==c {print $3}')
	[ "$have_port" = "$PG_PORT" ] || die "cluster $PG_VER/$PG_CLUSTER is on port $have_port, expected $PG_PORT"
	say "PostgreSQL $PG_VER cluster $PG_CLUSTER exists on port $PG_PORT"
else
	# UTF8, whatever the container's locale: the tests count characters.
	run pg_createcluster -p "$PG_PORT" -e UTF8 --locale C.UTF-8 "$PG_VER" "$PG_CLUSTER"
	say "created PostgreSQL $PG_VER cluster $PG_CLUSTER on port $PG_PORT"
fi

# --- PostgreSQL configuration -----------------------------------------------
restart=0
reload=0
conf=$PG_ETC/conf.d/deploybot-timescaledb.conf
conf_want="shared_preload_libraries = 'timescaledb'
timescaledb.telemetry_level = off"
if [ "$(cat "$conf" 2>/dev/null)" != "$conf_want" ]; then
	mkdir -p "$PG_ETC/conf.d"
	printf '%s\n' "$conf_want" >"$conf"
	restart=1
	say "wrote $conf (preload timescaledb)"
fi
hba=$PG_ETC/pg_hba.conf
if ! grep -qxF "$HBA_MARK" "$hba"; then
	tmp=$(mktemp)
	{
		printf '%s\n' "$HBA_MARK"
		printf 'host %s deploybot_migrator,deploybot_api 127.0.0.1/32 trust\n' "$TEST_DB"
		cat "$hba"
	} >"$tmp"
	cat "$tmp" >"$hba"
	rm -f "$tmp"
	reload=1
	say "added a pg_hba trust line for the test roles on 127.0.0.1 ($TEST_DB only)"
fi

if ! pg_lsclusters --no-header | awk -v v="$PG_VER" -v c="$PG_CLUSTER" '$1==v && $2==c {print $4}' | grep -qx online; then
	run pg_ctlcluster "$PG_VER" "$PG_CLUSTER" start
	say "started PostgreSQL"
elif [ "$restart" -eq 1 ]; then
	run pg_ctlcluster "$PG_VER" "$PG_CLUSTER" restart
	say "restarted PostgreSQL"
elif [ "$reload" -eq 1 ]; then
	run pg_ctlcluster "$PG_VER" "$PG_CLUSTER" reload
	say "reloaded PostgreSQL"
else
	say "PostgreSQL already running"
fi

# --- Test database and roles ------------------------------------------------
if [ "$(psql_pg -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$TEST_DB'")" = 1 ]; then
	say "database $TEST_DB already exists"
else
	run as_pg createdb -p "$PG_PORT" -E UTF8 --locale C.UTF-8 -T template0 "$TEST_DB"
	say "created database $TEST_DB"
fi
enc=$(psql_pg -d postgres -tAc "SELECT pg_encoding_to_char(encoding) FROM pg_database WHERE datname = '$TEST_DB'")
[ "$enc" = UTF8 ] || die "database $TEST_DB has encoding $enc, not UTF8; drop it and rerun"
# roles.sql.example is idempotent and must be the first command of its own
# session (CREATE EXTENSION timescaledb); psql opens a new session. The file
# goes in on stdin: postgres may not be able to read the checkout (a GitHub
# runner's home is mode 750).
run psql_pg -d "$TEST_DB" -f - <"$ROLES_SQL"
# The example file sets placeholder passwords; clear them so the only way in
# is the local trust line above.
run psql_pg -d "$TEST_DB" -c 'ALTER ROLE deploybot_migrator PASSWORD NULL' -c 'ALTER ROLE deploybot_api PASSWORD NULL'
say "applied roles.sql.example to $TEST_DB (deploybot_owner, deploybot_migrator, deploybot_api; timescaledb)"

# --- Node 24 and pnpm -------------------------------------------------------
if [ "$("$NODE_DIR/bin/node" --version 2>/dev/null || true)" = "v$NODE_VERSION" ]; then
	say "Node v$NODE_VERSION already in $NODE_DIR"
else
	tmp=$(mktemp -d)
	tarball=node-v$NODE_VERSION-linux-x64.tar.xz
	run curl -fsSL --retry 3 -o "$tmp/$tarball" "https://nodejs.org/dist/v$NODE_VERSION/$tarball"
	printf '%s  %s\n' "$NODE_SHA256" "$tmp/$tarball" | sha256sum -c --status || die "Node tarball checksum mismatch"
	rm -rf "$NODE_DIR"
	mkdir -p "$NODE_DIR"
	run tar -xJf "$tmp/$tarball" -C "$NODE_DIR" --strip-components=1 --no-same-owner
	rm -rf "$tmp"
	say "installed Node v$NODE_VERSION in $NODE_DIR (sha256 checked)"
fi
if [ "$("$NODE_DIR/bin/pnpm" --version 2>/dev/null || true)" = "$PNPM_VERSION" ]; then
	say "pnpm $PNPM_VERSION already installed"
else
	run env PATH="$NODE_DIR/bin:$PATH" npm install -g --prefix "$NODE_DIR" "pnpm@$PNPM_VERSION"
	say "installed pnpm $PNPM_VERSION"
fi

# --- Non-root test user -----------------------------------------------------
if id "$TEST_USER" >/dev/null 2>&1; then
	say "user $TEST_USER already exists"
else
	run useradd --create-home --shell /bin/bash "$TEST_USER"
	say "created user $TEST_USER"
fi
TEST_HOME=$(getent passwd "$TEST_USER" | cut -d: -f6)
# AGENTS.md puts Node 24 on PATH as $HOME/.local/opt/node/bin; link it for
# root and the test user.
for home in /root "$TEST_HOME"; do
	if [ "$(readlink "$home/.local/opt/node" 2>/dev/null || true)" != "$NODE_DIR" ]; then
		mkdir -p "$home/.local/opt"
		ln -sfn "$NODE_DIR" "$home/.local/opt/node"
		[ "$home" = /root ] || chown -R "$TEST_USER:" "$home/.local"
		say "linked $home/.local/opt/node -> $NODE_DIR"
	fi
done
# The checkouts belong to root; let git (Go's VCS stamping, which bot-agent's
# packaging test uses) read them as the test user.
for dir in "$REPO" "$(dirname "$REPO")/bot-agent"; do
	[ -d "$dir/.git" ] || continue
	# A GitHub runner's checkout is not readable by the test user; the
	# workflow runs the tests as the runner user instead.
	if ! runuser -u "$TEST_USER" -- test -r "$dir/.git"; then
		say "skipped the safe.directory entry for $dir: $TEST_USER cannot read it"
		continue
	fi
	if ! runuser -u "$TEST_USER" -- env HOME="$TEST_HOME" git config --global --get-all safe.directory | grep -qxF "$dir"; then
		run runuser -u "$TEST_USER" -- env HOME="$TEST_HOME" git config --global --add safe.directory "$dir"
		say "marked $dir as a safe git directory for $TEST_USER"
	fi
done
# Fetch the Go toolchain and modules for the test user once, so test runs
# start without downloads.
GO_BIN=$(command -v go || true)
if [ -n "$GO_BIN" ]; then
	for dir in "$REPO" "$(dirname "$REPO")/bot-agent"; do
		[ -f "$dir/go.mod" ] || continue
		# A GitHub runner's checkout is not readable by the test user; the
		# workflow runs the tests as the runner user instead.
		if ! runuser -u "$TEST_USER" -- test -r "$dir/go.mod"; then
			say "skipped the Go module download in $dir: $TEST_USER cannot read it"
			continue
		fi
		run runuser -u "$TEST_USER" -- env HOME="$TEST_HOME" PATH="$(dirname "$GO_BIN"):/usr/bin:/bin" \
			bash -c 'cd "$1" && go mod download' _ "$dir"
		say "Go modules ready for $TEST_USER in $dir"
	done
else
	say "go not found on PATH; skipped the Go module download"
fi

# --- Result -----------------------------------------------------------------
cat <<EOF
cloud-setup: done. Set these for the database tests:
DEPLOYBOT_TEST_DATABASE_URL=postgres://deploybot_migrator@127.0.0.1:$PG_PORT/$TEST_DB?sslmode=disable
DEPLOYBOT_TEST_API_DATABASE_URL=postgres://deploybot_api@127.0.0.1:$PG_PORT/$TEST_DB?sslmode=disable
cloud-setup: run tests as $TEST_USER, for example:
  runuser -u $TEST_USER -- env HOME=$TEST_HOME PATH=$TEST_HOME/.local/opt/node/bin:${GO_BIN:+$(dirname "$GO_BIN"):}/usr/bin:/bin \\
    DEPLOYBOT_TEST_DATABASE_URL=... DEPLOYBOT_TEST_API_DATABASE_URL=... go test -count=1 ./...
EOF
