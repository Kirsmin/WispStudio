#!/usr/bin/env bash
# 输出事件日志统计：各 type 事件数、唯一 blob 总量与数据库体积。
# 用法：scripts/stats.sh [Data/eventlog.db]
set -euo pipefail

db="${1:-Data/eventlog.db}"
if [[ ! -f "$db" ]]; then
  echo "数据库不存在: $db" >&2
  exit 1
fi

echo "数据库: $db"
echo

echo "== 事件类型分布（blob_bytes 为该 type 引用的唯一 blob 字节数）=="
sqlite3 -header -column "$db" <<'SQL'
WITH refs AS (
  SELECT e.type AS type, e.blob AS blob, LENGTH(b.data) AS bytes
  FROM events e
  JOIN blobs b ON b.hash = e.blob
  WHERE e.blob <> ''
  GROUP BY e.type, e.blob
),
blob_totals AS (
  SELECT type, COUNT(*) AS unique_blobs, SUM(bytes) AS blob_bytes
  FROM refs
  GROUP BY type
),
event_totals AS (
  SELECT type, COUNT(*) AS events
  FROM events
  GROUP BY type
)
SELECT e.type            AS type,
       e.events          AS events,
       COALESCE(b.unique_blobs, 0) AS unique_blobs,
       COALESCE(b.blob_bytes, 0)   AS blob_bytes
FROM event_totals e
LEFT JOIN blob_totals b ON b.type = e.type
ORDER BY e.events DESC, e.type ASC;
SQL

echo
echo "== 总量 =="
sqlite3 -header -column "$db" <<'SQL'
SELECT (SELECT COUNT(*) FROM events) AS events,
       (SELECT MIN(seq) FROM events) AS min_seq,
       (SELECT MAX(seq) FROM events) AS max_seq,
       (SELECT COUNT(*) FROM blobs)  AS blobs,
       (SELECT COALESCE(SUM(LENGTH(data)), 0) FROM blobs) AS blob_bytes,
       (SELECT COUNT(*) FROM blobs
         WHERE hash NOT IN (SELECT blob FROM events WHERE blob <> '')) AS orphan_blobs;
SQL

echo
echo "== seq 连续性 =="
sqlite3 -header -column "$db" <<'SQL'
SELECT COUNT(*) AS gaps
FROM (
  SELECT seq, LAG(seq) OVER (ORDER BY seq) AS prev
  FROM events
)
WHERE prev IS NOT NULL AND seq != prev + 1;
SQL

echo
echo "== 文件体积 =="
stat -c '%n %s bytes' "$db"
for suffix in -wal -shm; do
  if [[ -f "$db$suffix" ]]; then
    stat -c '%n %s bytes' "$db$suffix"
  fi
done
