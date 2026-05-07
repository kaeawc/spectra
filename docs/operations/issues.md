# Issues

Spectra issues are the operational layer of the recommendations engine.
Rules produce point-in-time findings; issues persist those findings across
snapshots so operators can track acknowledgement, dismissal, and fix history.

## Concepts

- **Rule** — a built-in Spectra check from `internal/rules`.
- **Finding** — one rule firing against one snapshot subject, such as an app,
  process, JDK, or host-level condition.
- **Issue** — the persisted lifecycle record for a finding identity.
- **Acknowledgement** — an operator has seen the issue and accepts that it
  remains active.
- **Dismissal** — the finding is intentionally suppressed. Later matching
  findings do not reopen or duplicate the dismissed issue.
- **Applied fix** — a recorded remediation attempt, including actor, command,
  output, exit code, and timestamp.

Findings are matched to issues by `(rule_id, machine_uuid, subject)` while
the existing issue is active. Active statuses are `open` and `acknowledged`.

## Lifecycle

```text
open -> acknowledged -> fixed -> closed
  \------------------> dismissed
```

`spectra issues check` evaluates rules and upserts matching active issues.
When the same active issue is observed again, Spectra refreshes
`last_seen_snapshot_id` and `updated_at` instead of creating a duplicate.

Dismissed issues are suppression records. A later matching finding is skipped
instead of reopened. If an operator wants to track that finding again, they
can move the issue back to `open`.

Fixed and closed issues are historical records. If the same finding appears
again after being fixed or closed, Spectra may create a new open issue because
the remediation did not hold or the condition returned.

## CLI

Evaluate rules without persisting:

```bash
spectra rules
spectra rules --json
spectra rules --snapshot <snapshot-id>
spectra rules --rules-config spectra.yml
```

Evaluate and persist issues:

```bash
spectra issues check
spectra issues check --json
spectra issues check --snapshot <snapshot-id>
spectra issues check --rules-config spectra.yml
```

List and transition issues:

```bash
spectra issues list
spectra issues list --status open
spectra issues list --json
spectra issues acknowledge <issue-id>
spectra issues dismiss <issue-id>
spectra issues update --status fixed <issue-id>
spectra issues update --status closed <issue-id>
```

Record and inspect fix history:

```bash
spectra issues record-fix \
  --applied-by "$USER" \
  --command "brew upgrade openjdk@21" \
  --output "upgraded" \
  --exit-code 0 \
  <issue-id>

spectra issues fix-history <issue-id>
spectra issues fix-history --json <issue-id>
```

## Storage

Issues are stored in SQLite through `internal/store`.

The `issues` table stores the canonical issue state:

- `id`
- `rule_id`
- `machine_uuid`
- `subject`
- `severity`
- `message`
- `fix`
- `status`
- `first_seen_snapshot_id`
- `last_seen_snapshot_id`
- `created_at`
- `updated_at`

The `applied_fixes` table stores remediation history:

- `id`
- `issue_id`
- `applied_at`
- `applied_by`
- `command`
- `output`
- `exit_code`

Snapshots are retained separately. Issues reference snapshot IDs rather than
duplicating snapshot content.

## Component boundary

Issue orchestration lives in `internal/issues.Service`. The service depends on
interfaces rather than concrete collectors or SQLite so lifecycle behavior can
be tested without touching the host machine:

- `Store` — issue rows, status transitions, upserts, and applied fixes.
- `SnapshotStore` — snapshot persistence used by live checks.
- `SnapshotSource` — live or stored snapshot retrieval.
- `Engine` — rule evaluation against a snapshot.

Unit tests use fakes for each boundary. Store-level tests cover SQLite
semantics such as dismissal suppression and fix-history persistence.

## Daemon and MCP

The daemon exposes issue operations over JSON-RPC:

- `rules.check`
- `issues.check`
- `issues.list`
- `issues.record`
- `issues.update`
- `issues.acknowledge`
- `issues.dismiss`
- `issues.fix.record`
- `issues.fix.list`

The MCP server exposes the same operational surface through the `issues` tool:

- `check`
- `list`
- `acknowledge`
- `dismiss`
- `record_fix`
- `fix_history`

## Overrides

Project-local `spectra.yml` files can disable built-in rules or override
their severity:

```yaml
rules:
  disabled:
    - app-unsigned
  severity:
    jvm-eol-version: high
    library-storage-footprint: low
```

Unknown rule IDs produce warnings so team configs do not silently drift.
