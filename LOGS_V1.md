# Shipyard Logs v1

Shipyard logs are stored locally as JSONL files under `~/.shipyard/logs/`.

## Defaults

- backend: JSONL
- initial source: `cron`
- initialization: automatic on first write
- retention: `14` days

## Layout

```text
~/.shipyard/
  logs.json
  logs/
    cron/
      2026-04-14.jsonl
```

## Event schema

Each log entry includes:

- `ts`
- `source`
- `level`
- `event`
- `message`
- `entityType`
- `entityId`
- `entityName`
- `runId`
- `data`

## CLI

- `shipyard logs`
- `shipyard logs list`
- `shipyard logs show`
- `shipyard logs tail`
- `shipyard logs prune`
- `shipyard logs config`
- `shipyard logs config set retention-days <n>`
