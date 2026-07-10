#!/usr/bin/env sh
# backup-verify.sh —— 恢复演练（差距清查 P0-2 "至少一次恢复演练记录"）
#
# 把最新一份备份恢复进一个**一次性临时 MySQL 容器**（绝不碰生产容器/数据卷），
# 断言核心表存在且有行，然后销毁容器。演练通过的输出粘进 runbook §5 留档。
#
# 用法：
#   ./backup-verify.sh                        # 用 /var/backups/laiai 最新一份
#   ./backup-verify.sh /path/to/laiai-x.sql.gz[.enc]   # 指定文件
#   加密备份需先 export LAIAI_BACKUP_PASSPHRASE=...
set -eu

BACKUP_DIR="${BACKUP_DIR:-/var/backups/laiai}"
FILE="${1:-$(ls -1 "$BACKUP_DIR"/laiai-*.sql.gz* 2>/dev/null | sort -r | head -1)}"
[ -n "$FILE" ] && [ -f "$FILE" ] || { echo "no backup file found (dir: $BACKUP_DIR)" >&2; exit 2; }

NAME="laiai-restore-drill-$$"
IMAGE="${MYSQL_IMAGE:-mysql:8.4}"
PW="drill-only-$(date +%s)" # 一次性容器的一次性口令，无敏感性

echo "== 恢复演练：$FILE -> 临时容器 $NAME ($IMAGE) =="
docker run -d --name "$NAME" -e MYSQL_ROOT_PASSWORD="$PW" "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT INT TERM

# 等 MySQL 就绪（一次性容器初始化约 10-30s）
i=0
until docker exec "$NAME" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqladmin ping -h localhost -uroot --silent' 2>/dev/null; do
  i=$((i+1)); [ "$i" -ge 60 ] && { echo "FATAL: temp mysql not ready after 120s" >&2; exit 1; }
  sleep 2
done

# 解压（可选解密）后灌入
case "$FILE" in
  *.enc)
    [ -n "${LAIAI_BACKUP_PASSPHRASE:-}" ] || { echo "encrypted backup: set LAIAI_BACKUP_PASSPHRASE" >&2; exit 2; }
    openssl enc -d -aes-256-cbc -pbkdf2 -pass env:LAIAI_BACKUP_PASSPHRASE -in "$FILE" | zcat ;;
  *) zcat "$FILE" ;;
esac | docker exec -i "$NAME" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot'

# 断言：库存在、核心表在、资金事实源有行结构（行数打印供人工核对）
echo "== 断言 =="
docker exec "$NAME" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "
  SELECT CONCAT(\"tables=\", COUNT(*)) FROM information_schema.tables WHERE table_schema=\"laiai\";
  SELECT CONCAT(\"journal_rows=\", COUNT(*)) FROM laiai.ledger_journal;
  SELECT CONCAT(\"users_rows=\", COUNT(*)) FROM laiai.users;
  SELECT CONCAT(\"max_migration=\", MAX(version)) FROM laiai.schema_migrations;"' 2>/dev/null || {
  echo "FATAL: assertion query failed（表缺失或 dump 损坏）" >&2; exit 1; }

TABLES=$(docker exec "$NAME" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=\"laiai\""')
[ "$TABLES" -ge 20 ] || { echo "FATAL: only $TABLES tables restored (expect ≥20)" >&2; exit 1; }

echo "== PASS：恢复演练通过（$TABLES 张表）。把本输出粘贴进 docs/生产runbook.md §5 留档 =="
