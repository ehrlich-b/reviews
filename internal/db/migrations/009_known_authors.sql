CREATE TABLE known_authors (
    username TEXT PRIMARY KEY,
    first_seen_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Seed from existing PR authors
INSERT OR IGNORE INTO known_authors (username) SELECT DISTINCT author FROM pull_requests WHERE author != '';
