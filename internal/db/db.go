package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Store is a SQLite-backed persistence layer.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at path and runs migrations.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite doesn't support concurrent writes
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS user_mappings (
			github_username TEXT PRIMARY KEY,
			slack_user_id   TEXT NOT NULL UNIQUE,
			github_token    TEXT NOT NULL,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS oauth_states (
			state         TEXT PRIMARY KEY,
			slack_user_id TEXT NOT NULL,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS pr_messages (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			repo             TEXT NOT NULL,
			pr_number        INTEGER NOT NULL,
			slack_user_id    TEXT NOT NULL,
			slack_message_ts TEXT NOT NULL UNIQUE,
			created_at       DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_pr_messages_repo_pr
			ON pr_messages(repo, pr_number);

		CREATE TABLE IF NOT EXISTS comment_messages (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			repo             TEXT NOT NULL,
			pr_number        INTEGER NOT NULL,
			comment_id       INTEGER NOT NULL,
			comment_type     TEXT NOT NULL,
			slack_user_id    TEXT NOT NULL,
			slack_message_ts TEXT NOT NULL UNIQUE,
			created_at       DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_comment_messages_ts
			ON comment_messages(slack_message_ts);
	`)
	return err
}

// --- User Mappings ---

// UpsertUserMapping stores or updates a GitHub username ↔ Slack user ID mapping.
func (s *Store) UpsertUserMapping(githubUsername, slackUserID, githubToken string) error {
	_, err := s.db.Exec(`
		INSERT INTO user_mappings (github_username, slack_user_id, github_token, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(github_username) DO UPDATE SET
			slack_user_id = excluded.slack_user_id,
			github_token  = excluded.github_token,
			updated_at    = CURRENT_TIMESTAMP
	`, githubUsername, slackUserID, githubToken)
	return err
}

// GetMappingByGitHubUsername returns the Slack user ID for a GitHub username, or "" if not found.
func (s *Store) GetMappingByGitHubUsername(githubUsername string) (slackUserID string, err error) {
	err = s.db.QueryRow(
		`SELECT slack_user_id FROM user_mappings WHERE github_username = ?`,
		githubUsername,
	).Scan(&slackUserID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return slackUserID, err
}

// UserMapping holds all fields for a mapped user.
type UserMapping struct {
	GitHubUsername string
	SlackUserID    string
	GitHubToken    string
}

// GetMappingBySlackUserID returns the full mapping for a Slack user ID.
func (s *Store) GetMappingBySlackUserID(slackUserID string) (*UserMapping, error) {
	var m UserMapping
	err := s.db.QueryRow(
		`SELECT github_username, slack_user_id, github_token FROM user_mappings WHERE slack_user_id = ?`,
		slackUserID,
	).Scan(&m.GitHubUsername, &m.SlackUserID, &m.GitHubToken)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// --- OAuth States ---

// SaveOAuthState stores a state token associated with a Slack user ID.
func (s *Store) SaveOAuthState(state, slackUserID string) error {
	_, err := s.db.Exec(
		`INSERT INTO oauth_states (state, slack_user_id) VALUES (?, ?)`,
		state, slackUserID,
	)
	return err
}

// ConsumeOAuthState retrieves and deletes a state token, returning the associated Slack user ID.
func (s *Store) ConsumeOAuthState(state string) (string, error) {
	var slackUserID string
	err := s.db.QueryRow(
		`SELECT slack_user_id FROM oauth_states WHERE state = ?`, state,
	).Scan(&slackUserID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, _ = s.db.Exec(`DELETE FROM oauth_states WHERE state = ?`, state)
	// Clean up states older than 1 hour.
	_, _ = s.db.Exec(`DELETE FROM oauth_states WHERE created_at < datetime('now', '-1 hour')`)
	return slackUserID, nil
}

// --- PR Messages ---

// SavePRMessage stores the Slack message TS for a review-requested DM so it can be edited later.
func (s *Store) SavePRMessage(repo string, prNumber int, slackUserID, messageTS string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO pr_messages (repo, pr_number, slack_user_id, slack_message_ts)
		VALUES (?, ?, ?, ?)
	`, repo, prNumber, slackUserID, messageTS)
	return err
}

// PRMessage represents a stored review-requested DM.
type PRMessage struct {
	SlackUserID string
	MessageTS   string
}

// GetPRMessages returns all stored DMs for a given PR (used when updating on merge/close).
func (s *Store) GetPRMessages(repo string, prNumber int) ([]PRMessage, error) {
	rows, err := s.db.Query(`
		SELECT slack_user_id, slack_message_ts
		FROM pr_messages WHERE repo = ? AND pr_number = ?
	`, repo, prNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []PRMessage
	for rows.Next() {
		var m PRMessage
		if err := rows.Scan(&m.SlackUserID, &m.MessageTS); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// --- Comment Messages ---

// SaveCommentMessage stores the Slack message TS for a comment DM.
func (s *Store) SaveCommentMessage(repo string, prNumber int, commentID int64, commentType, slackUserID, messageTS string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO comment_messages
			(repo, pr_number, comment_id, comment_type, slack_user_id, slack_message_ts)
		VALUES (?, ?, ?, ?, ?, ?)
	`, repo, prNumber, commentID, commentType, slackUserID, messageTS)
	return err
}

// CommentMessage represents a stored comment DM context.
type CommentMessage struct {
	Repo        string
	PRNumber    int
	CommentID   int64
	CommentType string
	SlackUserID string
}

// GetCommentMessage looks up the GitHub context for a given Slack message TS.
func (s *Store) GetCommentMessage(messageTS string) (*CommentMessage, error) {
	var m CommentMessage
	err := s.db.QueryRow(`
		SELECT repo, pr_number, comment_id, comment_type, slack_user_id
		FROM comment_messages WHERE slack_message_ts = ?
	`, messageTS).Scan(&m.Repo, &m.PRNumber, &m.CommentID, &m.CommentType, &m.SlackUserID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
