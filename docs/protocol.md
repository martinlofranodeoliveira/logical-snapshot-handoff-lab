# Protocol subset and inspection

Authoritative sources:

- https://www.postgresql.org/docs/17/logicaldecoding-explanation.html
- https://www.postgresql.org/docs/17/protocol-logicalrep-message-formats.html
- https://github.com/jackc/pglogrepl
- https://github.com/jackc/pgx
- https://www.sqlite.org/atomiccommit.html

This implementation uses pgx/pgconn for PostgreSQL transport and pglogrepl for logical replication commands and message decoding. It does not substitute a handwritten parser. Synthetic wire encoders in tests are independent fixtures, not runtime decoders.

Requested options: proto_version=1, publication_names=lab_pub, binary=false, streaming=false, messages=false, two_phase=false. Supported logical messages:

| Message | Rule |
|---|---|
| Begin | no active transaction; retain its final commit-record LSN |
| Relation | exact public.items OID/name/namespace, DEFAULT identity, exactly three columns and exact flags/OIDs/modifiers |
| Insert | complete text tuple with three columns |
| Update | complete new text tuple; any old tuple rejected |
| Delete | K old-key tuple, three positions, text ID and NULL non-key positions |
| Truncate | exactly items, no CASCADE or RESTART IDENTITY options |
| Commit | active transaction; flags zero; CommitLSN equals Begin.FinalLSN; EndLSN greater |

Other logical messages fail closed. Multiple transactions can share transport packets in PostgreSQL's protocol, but pglogrepl is fed each actual XLogData WALData message produced by the owned server. PostgreSQL relation metadata is bounded to the one admitted relation. A new connection starts a new dictionary. Binary, NULL required values, unchanged TOAST values and noncanonical int8 text are rejected. Labels are short enough that the fixed source fixture does not need TOAST placeholders.

Raw supported transaction captures include the latest validated relation message when it was sent before Begin, then the actual transaction frames. Capture is returned as JSON base64 byte arrays. The controller feeds that independently captured sequence through a new decoder, applies it to a separate SQLite image initialized from the bootstrap baseline, then applies the decoded transaction twice and checks explicit expected rows, checkpoint and scalar count. Captures are not written to an unbounded log.

Transport keepalives may request feedback but cannot advance the checkpoint. Their server WAL end/time are ignored for replay semantics. The only checkpoint candidates are the initial consistent point and supported Commit.EndLSN. A worker can be killed after durable commit but before feedback by withholding the `feedback` barrier release. On restart, the confirmed slot point remains older; replay of that endpoint is counted as a diagnostic duplicate, not another durable application.

The JSON control protocol is local, inherited and synchronous. It is not authenticated for exposure to a network and must not be repurposed as a network service without a new design/contract.
