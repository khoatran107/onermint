# Runtime logs

`monitoring/` chứa Grafana, Prometheus, Loki và Alloy. Node ghi `events.jsonl`
trong `local-runs/<test>/node-*`; Alloy đọc các file này để hiển thị trên Grafana.
Finalized block nằm trong LevelDB, không nằm trong log.

`events.jsonl` có hai format, và dashboard đọc được cả hai:

| Format | Khi nào | Thời gian | Step | Lý do reject |
|---|---|---|---|---|
| Rút gọn | `Trace: false` | `Ts`, `Time` | `"PROPOSE"`, `"PREVOTE"`, `"PRECOMMIT"` | `Detail` |
| Trace | `Trace: true` (mọi config T1–T8) | `WallTime` | `1`, `2`, `3` | `Reason` |

Alloy lấy timestamp của mỗi dòng từ `Ts`, hoặc từ `WallTime` nếu không có `Ts`,
nên thứ tự các step trên Grafana đúng với thời điểm node ghi log. Query LogQL
đổi `Step` dạng số sang tên trước khi lọc, bằng
`label_format step=...` ghi vào một label **mới**. Ghi đè chính label `Step`
làm bộ lọc `Step="PROPOSE"` phía sau không khớp.
