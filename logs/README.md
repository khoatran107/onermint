# Runtime logs

`monitoring/` chứa Grafana, Prometheus, Loki và Alloy. Node ghi `events.jsonl`
trong `local-runs/<test>/node-*`; Alloy đọc các file này để hiển thị trên Grafana.
Finalized block nằm trong LevelDB, không nằm trong log.
