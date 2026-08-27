package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

const sessionSelectCols = `id, name, group_name, parent_id, issue_key, branch, repo_name, remote_host, worktree_path, working_directory, tmux_session, backend, mutagen_sync_id, local_path, agent_name, agent_model, status, mode, trigger_source, trigger_event_id, autonomous_config_json, tunnels_json, aws_config_json, agent_session_id, agent_session_path, agent_title, agent_ended, hook_state, hook_state_message, hook_state_source, hook_state_seq, hook_state_at, created_at, updated_at`

type sessionRowScanner interface {
	Scan(dest ...any) error
}

func scanSession(scanner sessionRowScanner) (domain.Session, error) {
	var s domain.Session
	// NullString, not string: rows written before these columns existed hold
	// NULL, and scanning NULL into a string fails the entire query. In List that
	// meant one such row returned zero sessions and an empty dashboard.
	var statusStr, modeStr sql.NullString
	var name, groupName, parentID, issueKey, branch, repoName, remoteHost, worktreePath, workingDir, tmuxSession, backend, mutagenSyncID, localPath, agentName, agentModel, triggerSource, triggerEventID, autonomousConfigJSON, tunnelsJSON, awsConfigJSON, agentSessionID, agentSessionPath, agentTitle, agentEnded, hookState, hookStateMessage, hookStateSource sql.NullString
	var hookStateSeq sql.NullInt64
	var createdAt, updatedAt, hookStateAt sql.NullTime
	err := scanner.Scan(
		&s.ID, &name, &groupName, &parentID, &issueKey, &branch, &repoName, &remoteHost, &worktreePath, &workingDir, &tmuxSession, &backend, &mutagenSyncID, &localPath, &agentName, &agentModel, &statusStr, &modeStr, &triggerSource, &triggerEventID, &autonomousConfigJSON, &tunnelsJSON, &awsConfigJSON, &agentSessionID, &agentSessionPath, &agentTitle, &agentEnded, &hookState, &hookStateMessage, &hookStateSource, &hookStateSeq, &hookStateAt, &createdAt, &updatedAt)
	if err != nil {
		return s, err
	}

	s.Name = name.String
	s.Group = groupName.String
	s.ParentID = parentID.String
	s.IssueKey = issueKey.String
	s.Branch = branch.String
	s.RepoName = repoName.String
	s.RemoteHost = remoteHost.String
	s.WorktreePath = worktreePath.String
	s.WorkingDirectory = workingDir.String
	s.TmuxSession = tmuxSession.String
	s.Backend = backend.String
	s.MutagenSyncID = mutagenSyncID.String
	s.LocalPath = localPath.String
	s.AgentName = agentName.String
	s.AgentModel = agentModel.String
	s.Status = domain.SessionStatus(statusStr.String)
	if modeStr.String == "" {
		s.Mode = domain.SessionModeInteractive
	} else {
		s.Mode = domain.SessionMode(modeStr.String)
	}
	s.TriggerSource = triggerSource.String
	s.TriggerEventID = triggerEventID.String
	if autonomousConfigJSON.Valid && autonomousConfigJSON.String != "" {
		var ac domain.AutonomousConfig
		if uerr := json.Unmarshal([]byte(autonomousConfigJSON.String), &ac); uerr == nil {
			s.AutonomousConfig = &ac
		}
	}
	if tunnelsJSON.Valid && tunnelsJSON.String != "" {
		if uerr := json.Unmarshal([]byte(tunnelsJSON.String), &s.Tunnels); uerr != nil {
			return s, fmt.Errorf("failed to decode session tunnels: %w", uerr)
		}
	}
	if awsConfigJSON.Valid && awsConfigJSON.String != "" {
		var cfg domain.AWSConfig
		if uerr := json.Unmarshal([]byte(awsConfigJSON.String), &cfg); uerr == nil {
			s.AWSConfig = &cfg
		}
	}
	s.AgentSessionID = agentSessionID.String
	s.AgentSessionPath = agentSessionPath.String
	s.AgentTitle = agentTitle.String
	s.AgentEnded = agentEnded.String == "1"
	s.HookState = domain.AgentState(hookState.String)
	s.HookStateMessage = hookStateMessage.String
	s.HookStateSource = hookStateSource.String
	s.HookStateSeq = hookStateSeq.Int64
	s.HookStateAt = hookStateAt.Time
	s.CreatedAt = createdAt.Time
	s.UpdatedAt = updatedAt.Time
	return s, nil
}
