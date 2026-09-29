# Builds packer.exe and unpacker.exe.
#
#   .\build.ps1
#
# Two binaries, one direction each:
#
#   unpacker.exe  a .pack file  -> a folder of files beside it
#   packer.exe    a folder      -> a .pack file beside it
#
# Drop the right one on the job. They are separate programs rather than one
# program with a mode flag because the name is the instruction: dragging a
# folder onto packer.exe needs no argument, no flag and no menu, and there is
# no way for the two directions to be confused for one another.
#
# Deliberately CONSOLE subsystem binaries, not windowsgui ones. That is what
# makes dropping a file on the exe useful: Windows opens a cmd window for the
# process, which is where the progress and the result are printed. A windowsgui
# build would have no window at all and its output would go nowhere.
#
# -s -w strip the symbol table and DWARF data, which takes a 3.2 MB build down
# to about 1.8 MB without changing behavior.

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

Write-Host 'Running tests...' -ForegroundColor Cyan
go test ./...

if ($LASTEXITCODE -ne 0) { throw 'tests failed, not building' }

$targets = @(
    @{ Dir = 'cmd\unpacker'; Exe = 'unpacker.exe'; What = 'a .pack  -> a folder' },
    @{ Dir = 'cmd\packer';   Exe = 'packer.exe';   What = 'a folder -> a .pack'  }
)

foreach ($t in $targets) {
    Write-Host "Building $($t.Exe)..." -ForegroundColor Cyan
    go build -ldflags '-s -w' -o $t.Exe "./$($t.Dir)"

    if ($LASTEXITCODE -ne 0) { throw "build failed: $($t.Exe)" }
}

Write-Host ''
foreach ($t in $targets) {
    $size = [math]::Round((Get-Item $t.Exe).Length / 1MB, 2)
    Write-Host ("  {0,-14} {1,6} MB   {2}" -f $t.Exe, $size, $t.What) -ForegroundColor Green
}
Write-Host ''
Write-Host "In $PSScriptRoot. Drag a .pack onto unpacker.exe, or a folder onto packer.exe." -ForegroundColor DarkGray
