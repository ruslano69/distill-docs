# Build script for distill-docs (PowerShell)
# Builds: distill, distill-server
$ErrorActionPreference = "Stop"

$VersionBase = "0.1"
$Patch = (git rev-list --count HEAD 2>$null)
if (-not $Patch) { $Patch = "0" }
$Version = "$VersionBase.$Patch"
$LdFlags = "-s -w -X github.com/ruslano69/distill-docs/internal/version.Version=$Version"

$Binaries = @(
    @{ Name = "distill";        Cmd = ".\cmd\distill" },
    @{ Name = "distill-server"; Cmd = ".\cmd\distill-server" }
)

Write-Host "-> Building distill-docs v$Version..."
foreach ($b in $Binaries) {
    go build -ldflags "$LdFlags" -o "$($b.Name).exe" $b.Cmd
    Write-Host "  ok $($b.Name)"
}
Write-Host ""
Write-Host "Built. Try:"
Write-Host "  .\distill.exe --db .knowledge\docs.sqlite init"
Write-Host "  .\distill-server.exe --root .distill publish --name 2026.07 --channel stable"
