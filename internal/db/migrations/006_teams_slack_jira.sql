-- Named teams (replaces flat team_members)
CREATE TABLE teams (
    name TEXT PRIMARY KEY,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE team_memberships (
    team_name TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    username TEXT NOT NULL,
    added_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (team_name, username)
);

-- Migrate existing team_members into a "team" team
INSERT INTO teams (name) SELECT 'team' WHERE EXISTS (SELECT 1 FROM team_members LIMIT 1);
INSERT INTO team_memberships (team_name, username) SELECT 'team', username FROM team_members;
DROP TABLE team_members;

-- Slack user mapping (github username -> slack user ID + timezone)
CREATE TABLE slack_mappings (
    github_username TEXT PRIMARY KEY,
    slack_user_id TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'America/New_York'
);

-- Nag tracking (one row per PR, upserted daily)
CREATE TABLE nag_log (
    pr_key TEXT PRIMARY KEY,
    nagged_at TEXT NOT NULL
);

-- Cached Jira issue data
CREATE TABLE jira_issues (
    key TEXT PRIMARY KEY,
    summary TEXT NOT NULL,
    status TEXT NOT NULL,
    epic_key TEXT,
    epic_summary TEXT,
    synced_at TEXT NOT NULL
);
