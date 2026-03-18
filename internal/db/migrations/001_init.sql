CREATE TABLE pull_requests (
    id INTEGER PRIMARY KEY,
    repo TEXT NOT NULL,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    author TEXT NOT NULL,
    author_avatar TEXT,
    url TEXT NOT NULL,
    draft INTEGER NOT NULL DEFAULT 0,
    comment_count INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,

    ticket_key TEXT,
    ci_status TEXT,
    review_status TEXT,
    triage_bucket TEXT NOT NULL,
    triage_reason TEXT,
    last_commit_at TEXT,
    last_review_activity_at TEXT,
    approvers TEXT,

    synced_at TEXT NOT NULL,
    UNIQUE(repo, number)
);

CREATE TABLE sync_state (
    repo TEXT PRIMARY KEY,
    last_sync TEXT NOT NULL,
    etag TEXT
);
