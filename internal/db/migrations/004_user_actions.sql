CREATE TABLE user_actions (
    pr_key TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    commit_hash TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE team_members (
    username TEXT PRIMARY KEY,
    added_at TEXT NOT NULL DEFAULT (datetime('now'))
);
