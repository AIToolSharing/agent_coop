# coop-worker-tasks.ps1: one Task Scheduler task per agent kind, at the logon of this user, that
# runs coop-worker.ps1 hidden. Run it as the user whose CLIs are signed in.
#   pwsh -File deploy/coop-worker-tasks.ps1 -Root W:\coop\workers -Kinds claude,codex
# The worker directory of a kind is <Root>\<kind>, with the .coop file of its session
# (coop session workers --agent <kind>). The script goes to ~\.local\bin\coop-worker.ps1.
param(
  [Parameter(Mandatory)][string]$Root,
  [string[]]$Kinds = @('claude', 'codex', 'copilot', 'gemini')
)
# pwsh -File passes "claude,codex" as one string.
$Kinds = $Kinds | ForEach-Object { $_ -split ',' }
$bin = Join-Path $HOME '.local\bin'
New-Item -ItemType Directory -Force $bin | Out-Null
Copy-Item (Join-Path $PSScriptRoot 'coop-worker.ps1') (Join-Path $bin 'coop-worker.ps1') -Force
$pwsh = (Get-Command pwsh).Source
foreach ($k in $Kinds) {
  $dir = Join-Path $Root $k
  if (-not (Test-Path (Join-Path $dir '.coop'))) { throw "$dir has no .coop: run coop session <name> --agent $k there" }
  $action = New-ScheduledTaskAction -Execute $pwsh -Argument "-NoProfile -WindowStyle Hidden -File `"$bin\coop-worker.ps1`" `"$dir`" $k"
  $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
  $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -MultipleInstances IgnoreNew
  Register-ScheduledTask -TaskName "coop-worker $k" -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
  Start-ScheduledTask -TaskName "coop-worker $k"
  "coop-worker ${k}: $((Get-ScheduledTask -TaskName "coop-worker $k").State)"
}
