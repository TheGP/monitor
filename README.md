# monitor

PM2-managed process watchdog. Every minute logs top CPU/RAM consumers with parent process info, and sends a Telegram alert when memory or CPU crosses a threshold.

## Logging

- **PM2 stdout** (`pm2 logs monitor`) — one compact line per tick only:
  `12:03 RAM 72% 5.6/8G | CPU 34% | top: node 800MB(pid 1234←pm2)`
- **`logs/monitor-YYYY-MM-DD.log`** — full tables per tick (top CPU + top RAM with parent/user/cmd)
- **`logs/alert-<timestamp>.log`** — full dump with cmdlines on every threshold breach

All logs are written and rotated by the app itself. No pm2-logrotate needed.

## Deploy (server)

```bash
cd ~/apps/monitor
bash deploy.sh
```

## Credentials

Reads from `../.env` first (shared apps folder), then local `.env`.
Required: `DEVELOPER_TELEGRAM_BOT_TOKEN`, `DEVELOPER_TELEGRAM_CHAT_ID`.

## Config (`.env`)

| Variable | Default | Description |
|---|---|---|
| `INTERVAL_SECONDS` | `60` | How often to sample |
| `MEM_ALERT_PCT` | `85` | Alert when RAM used ≥ this % |
| `CPU_ALERT_PCT` | `90` | Alert when total CPU ≥ this % |
| `TOP_N` | `10` | Processes to show per table |
| `ALERT_COOLDOWN_MIN` | `15` | Min minutes between alerts |
| `LOG_DIR` | `./logs` | Where to write log files |
| `LOG_RETENTION_DAYS` | `2` | Delete logs older than N days |

## Useful commands

```bash
pm2 logs monitor                              # live compact heartbeat
pm2 status                                    # check it's running
tail -f logs/monitor-$(date +%F).log          # today's full tables
ls -lt logs/alert-*.log | head                # recent alerts
```
