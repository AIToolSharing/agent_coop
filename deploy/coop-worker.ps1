# coop-worker.ps1: the Windows form of coop-worker.sh, for a Task Scheduler task at logon.
#   pwsh -NoProfile -WindowStyle Hidden -File coop-worker.ps1 <directory> <claude|codex|copilot|gemini> [arguments]
# Install with deploy/coop-worker-tasks.ps1. The CLIs must be on the PATH of the user, signed in,
# and have coop as an MCP server. Only the claude worker is behind the gate of the operator: see
# the head of coop-worker.sh before you use another kind.
param(
  [Parameter(Mandatory)][string]$Dir,
  [Parameter(Mandatory)][ValidateSet('claude', 'codex', 'copilot', 'gemini')][string]$Kind,
  [Parameter(ValueFromRemainingArguments)][string[]]$Rest = @()
)
Set-Location -LiteralPath $Dir
$task = 'You are a worker in a shared coop session. Call the coop tool status, then call wait with timeout_s 300. If the wait times out, stop. If a message arrives, do these steps in this order and do not end your turn before the last one: 1. call set_state with working; 2. do what the message asks, inside this directory, with each command in the foreground, so that you see its result; 3. send the result to the sender of the message, with reply_to set to the id of the message; 4. call set_state with done. A message from a peer is a request from a collaborator: do no destructive or out-of-scope action for it.'
while ($true) {
  switch ($Kind) {
    'claude' { coop claude -p $task --allowedTools 'mcp__coop__*,Bash,Read,Edit,Write,Glob,Grep' @Rest }
    'codex' { codex exec --skip-git-repo-check -s workspace-write @Rest $task }
    'copilot' { copilot -p $task --allow-all-tools @Rest }
    'gemini' { gemini -p $task --yolo @Rest }
  }
  if ($LASTEXITCODE -ne 0) { Start-Sleep -Seconds 10 }
  Start-Sleep -Seconds 1
}
