#!/bin/sh
set -eu
mkdir /tmp/sqlite-source
curl --fail --silent --show-error --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 --max-filesize 4000000 https://www.sqlite.org/2026/sqlite-autoconf-3540100.tar.gz -o /tmp/sqlite.tar.gz
printf '926ef8d3eab93fbf6e06011901273eec076b25594a4e1cdadd50c987f4f6d64e  /tmp/sqlite.tar.gz\n' | sha256sum -c -
tar -xzf /tmp/sqlite.tar.gz -C /tmp/sqlite-source --strip-components=1
cd /tmp/sqlite-source
./configure --prefix=/opt/sqlite --enable-fts5 --disable-static
make -j2
make install
printf '/opt/sqlite/lib\n' > /etc/ld.so.conf.d/00-lsh-sqlite.conf
ldconfig
version=$(/opt/sqlite/bin/sqlite3 :memory: 'select sqlite_version();')
source=$(/opt/sqlite/bin/sqlite3 :memory: 'select sqlite_source_id();')
test "$version" = '3.54.1'
test "$source" = '2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76'
/opt/sqlite/bin/sqlite3 :memory: <<'SQL' | grep -x '1'
create virtual table q using fts5(a);
insert into q values ('synthetic');
select count(*) from q where q match 'synthetic';
SQL
rm -rf /tmp/sqlite-source /tmp/sqlite.tar.gz
