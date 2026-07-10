#!/usr/bin/env sh
# backup.sh —— LAIAI-NEXT MySQL 每日备份（上线冲刺批次 0.2；差距清查 P0-2）
#
# 设计：
#   - 宿主机 cron 调用；dump 走 docker exec，口令用容器内 $MYSQL_ROOT_PASSWORD 环境变量
#     （绝不出现在命令行/ps —— 与 compose healthcheck 同纪律，红线 7）
#   - gzip 压缩 + 完整性双检（gzip -t + 末行 "Dump completed" 哨兵）
#   - 可选加密：设 LAIAI_BACKUP_PASSPHRASE 时 openssl AES-256 加密（生产建议必开；
#     调试机可不设）。密码短语放 root 只读的 env 文件，不进本脚本
#   - 保留最近 RETAIN 份（默认 14），过期自动清
#   - 任何一步失败 = 非零退出且不留下半截文件（cron 邮件/日志可见）
#
# 用法：
#   ./backup.sh                          # 备份到默认 /var/backups/laiai
#   BACKUP_DIR=/data/bk ./backup.sh      # 自定义目录
#   crontab 示例（每日 04:10，日志追加）：
#     10 4 * * * /opt/laiai-next/infra/backup.sh >> /var/log/laiai-backup.log 2>&1
#
# 异地副本（后续，见 runbook §5）：本脚本只做本地层；R2/rclone 异地为第二层，
# 上线后接入（跨云容灾纪律，设计 §7）。
set -eu

CONTAINER="${MYSQL_CONTAINER:-laiai-mysql}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/laiai}"
RETAIN="${RETAIN:-14}"
STAMP=$(date +%Y%m%d-%H%M%S)
TMP="$BACKUP_DIR/.laiai-$STAMP.sql.gz.part"
OUT="$BACKUP_DIR/laiai-$STAMP.sql.gz"

mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR" # 备份含全量业务数据，目录仅 root

echo "[$(date -u +%FT%TZ)] backup start -> $OUT"

# --single-transaction：InnoDB 一致性快照，不锁表；--routines 带存储过程
docker exec "$CONTAINER" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --routines --databases laiai' \
  | gzip > "$TMP"

# 完整性双检：gzip 结构 + mysqldump 完成哨兵（半截 dump 不会有这行）
gzip -t "$TMP"
if ! zcat "$TMP" | tail -1 | grep -q "Dump completed"; then
  rm -f "$TMP"
  echo "FATAL: dump incomplete (no 'Dump completed' sentinel)" >&2
  exit 1
fi

# 可选加密（生产必开）：openssl AES-256-CBC + PBKDF2
if [ -n "${LAIAI_BACKUP_PASSPHRASE:-}" ]; then
  openssl enc -aes-256-cbc -pbkdf2 -salt -pass env:LAIAI_BACKUP_PASSPHRASE -in "$TMP" -out "$OUT.enc"
  rm -f "$TMP"
  OUT="$OUT.enc"
else
  mv "$TMP" "$OUT"
fi
chmod 600 "$OUT"

SIZE=$(du -h "$OUT" | cut -f1)
echo "[$(date -u +%FT%TZ)] backup ok: $OUT ($SIZE)"

# 保留策略：只留最近 RETAIN 份（按文件名时间戳排序）
ls -1 "$BACKUP_DIR"/laiai-*.sql.gz* 2>/dev/null | sort -r | tail -n +$((RETAIN + 1)) | while read -r old; do
  rm -f "$old"
  echo "pruned: $old"
done

echo "[$(date -u +%FT%TZ)] done. kept $(ls -1 "$BACKUP_DIR"/laiai-*.sql.gz* 2>/dev/null | wc -l)/$RETAIN"
