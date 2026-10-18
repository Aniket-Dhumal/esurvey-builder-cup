<#
.SYNOPSIS
    Gives back the disk space Docker Desktop is holding on Windows, and stops
    it building up again.

.DESCRIPTION
    Docker Desktop on WSL2 keeps everything -- images, build cache, volumes,
    container logs -- inside one virtual disk:

        %LOCALAPPDATA%\Docker\wsl\disk\docker_data.vhdx

    That file grows to fit whatever is inside it and never shrinks back on its
    own. This is the part that surprises people: `docker system prune` frees
    space *inside* the vhdx, and Windows sees no change at all. Prune, watch
    C: stay full, prune again. The high-water mark only ever rises, so the
    drive fills over weeks and then a build fails at 0 bytes free.

    Two things fix that, and this script does both:

      1. Prunes what Docker is no longer using.
      2. Marks the vhdx sparse, so freed blocks are returned to Windows from
         then on -- including automatically, next time.

    Step 2 needs the WSL virtual machine stopped, which is why the script
    stops Docker Desktop first and offers to start it again at the end.

.PARAMETER KeepImages
    Prune build cache and dangling images only, leaving tagged images alone.
    Slower to recover from; use it when the machine is offline or a rebuild
    would be inconvenient.

.NOTES
    Named volumes are never touched, by design and without an override. The
    postgres-data volume is the officer roll, the parcel registry and every
    assessment that has been filed. `docker system prune --volumes` would take
    all of it, so this script does not offer that option; drop the database
    deliberately with `docker compose down -v` if that is really what you want.

    Run from an elevated PowerShell:
        powershell -ExecutionPolicy Bypass -File scripts\reclaim-docker-disk.ps1
#>

[CmdletBinding()]
param(
    [switch]$KeepImages
)

$ErrorActionPreference = 'Stop'

function Get-FreeGB { [math]::Round((Get-PSDrive C).Free / 1GB, 2) }

function Get-VhdxGB {
    $p = Join-Path $env:LOCALAPPDATA 'Docker\wsl\disk\docker_data.vhdx'
    if (Test-Path $p) { [math]::Round((Get-Item $p).Length / 1GB, 2) } else { $null }
}

function Invoke-Wsl {
    # wsl.exe hangs indefinitely when the VM is wedged, and a hung call here
    # would leave Docker stopped with no explanation. A job with a timeout
    # turns that into a message.
    param([string[]]$Arguments, [int]$TimeoutSeconds = 120)

    $job = Start-Job -ScriptBlock { & wsl.exe @using:Arguments 2>&1 }
    if (Wait-Job $job -Timeout $TimeoutSeconds) {
        $out = Receive-Job $job
        Remove-Job $job -Force
        return (($out -join "`n") -replace "`0", '')
    }
    Stop-Job $job; Remove-Job $job -Force
    throw "wsl $($Arguments -join ' ') did not answer within $TimeoutSeconds seconds. The WSL VM is wedged; reboot and run this again."
}

$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

Write-Host ''
Write-Host 'Docker disk reclaim' -ForegroundColor Cyan
Write-Host ('  C: free      {0} GB' -f (Get-FreeGB))
Write-Host ('  vhdx         {0} GB' -f (Get-VhdxGB))
Write-Host ('  elevated     {0}' -f $isAdmin)
Write-Host ''

# --- 1. prune, while the engine is still up ---------------------------------

$engineUp = $false
try { docker version --format '{{.Server.Version}}' *> $null; $engineUp = $? } catch { }

if ($engineUp) {
    Write-Host 'Reclaiming inside the vhdx...' -ForegroundColor Cyan
    docker system df

    # Build cache is nearly always the bulk of it: every rebuild of the Go
    # image leaves its layers behind, and nothing ever collects them.
    docker builder prune --all --force | Select-Object -Last 1

    if ($KeepImages) {
        docker image prune --force | Select-Object -Last 1
    } else {
        # --all takes images no container currently uses. They are all
        # rebuildable or re-pullable; nothing here is data.
        docker image prune --all --force | Select-Object -Last 1
    }

    # Anonymous volumes only. Named ones -- postgres-data, redis-data,
    # evidence -- are not dangling and are not touched.
    docker volume prune --force | Select-Object -Last 1
    Write-Host ''
} else {
    Write-Host 'Docker engine is not answering; skipping the prune and going' -ForegroundColor Yellow
    Write-Host 'straight to compaction, which is what actually returns space.' -ForegroundColor Yellow
    Write-Host ''
}

# --- 2. stop everything holding the disk ------------------------------------

Write-Host 'Stopping Docker Desktop and WSL...' -ForegroundColor Cyan
Get-Process -Name 'Docker Desktop', 'com.docker.backend' -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 4

# Orphaned wsl.exe clients from earlier timed-out calls will block a shutdown
# forever, and the symptom is indistinguishable from a wedged VM.
Get-Process -Name wsl -ErrorAction SilentlyContinue |
    Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

Invoke-Wsl @('--shutdown') -TimeoutSeconds 90 | Out-Null
Start-Sleep -Seconds 6

if (Get-Process -Name vmmemWSL -ErrorAction SilentlyContinue) {
    throw 'The WSL VM is still running after --shutdown. Reboot and run this again.'
}

# --- 3. the step that actually gives space back to Windows ------------------

Write-Host 'Marking the vhdx sparse so freed blocks return to C:...' -ForegroundColor Cyan
if (-not $isAdmin) {
    Write-Host 'Not elevated -- this step needs an administrator PowerShell.' -ForegroundColor Yellow
} else {
    $before = Get-VhdxGB
    try {
        Invoke-Wsl @('--manage', 'docker-desktop', '--set-sparse', 'true') -TimeoutSeconds 900 |
            Write-Host
        $after = Get-VhdxGB
        Write-Host ('  vhdx {0} GB -> {1} GB' -f $before, $after) -ForegroundColor Green
    } catch {
        # Older WSL builds have no --manage. Optimize-VHD is the fallback, and
        # it needs the Hyper-V module rather than the Hyper-V role.
        Write-Host ('  --set-sparse unavailable: {0}' -f $_.Exception.Message) -ForegroundColor Yellow
        Write-Host '  Falling back to Optimize-VHD.' -ForegroundColor Yellow
        $vhdx = Join-Path $env:LOCALAPPDATA 'Docker\wsl\disk\docker_data.vhdx'
        Optimize-VHD -Path $vhdx -Mode Full
        Write-Host ('  vhdx {0} GB -> {1} GB' -f $before, (Get-VhdxGB)) -ForegroundColor Green
    }
}

Write-Host ''
Write-Host ('C: free now {0} GB' -f (Get-FreeGB)) -ForegroundColor Green
Write-Host ''
Write-Host 'Start Docker Desktop again when ready. Container logs are capped'
Write-Host 'in docker-compose.yml, so the vhdx should stop creeping upward.'
