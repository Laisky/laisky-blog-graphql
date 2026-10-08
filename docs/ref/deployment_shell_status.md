# Deployment shell status references

Verified 2026-10-08 for the pinned workflow versions.

## SSH action and shell behavior

- [ssh-action v1.0.3 inputs](https://github.com/appleboy/ssh-action/blob/v1.0.3/action.yml):
  `script_stop` is optional and was omitted by this repository.
- [Pinned Dockerfile](https://github.com/appleboy/ssh-action/blob/v1.0.3/Dockerfile)
  selects drone-ssh 1.7.3.
- [drone-ssh 1.7.3 implementation](https://github.com/appleboy/drone-ssh/blob/v1.7.3/plugin.go):
  per-line stop checks are injected only when ScriptStop is enabled. Keep that
  option unset for a Bash heredoc; an explicit Bash process propagates its status
  without injecting lines into multiline syntax.
- [Local action entrypoint](https://github.com/appleboy/ssh-action/blob/v1.0.3/entrypoint.sh):
  local strict options do not configure the remote shell. A successful final
  diagnostic command can mask an earlier failure in a bare command list.

## Docker Compose

- [Compose service volumes](https://docs.docker.com/reference/compose-file/services/#volumes):
  modern bind-mount syntax includes `create_host_path`.
- [Compose up](https://docs.docker.com/reference/cli/docker/compose/up/):
  errors return nonzero; `--no-deps` confines linked-service startup,
  `--pull never` avoids retrieving another image during recovery, and `--wait`
  waits for running or healthy status. Without a configured health check, running
  status alone is not proof that the application works.

The incident's exact log and behavioral reproduction are recorded in
`docs/rca/deployment_false_success_20261008.md`.
