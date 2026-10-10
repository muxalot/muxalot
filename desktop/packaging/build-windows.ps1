# Build dist\muxalot-desktop_<version>_windows-amd64.zip and smoke it. Run from any
# checkout on Windows (the release job does this):
#   powershell -File desktop\packaging\build-windows.ps1 [-Version 1.2.3]
# The smoke launches the exe on a clean config dir (APPDATA pointed at a temp
# dir) and requires it to still be running 25 s later: 20 s is when watchLoad
# gives up on a webview that never got ready, so surviving 25 s means the
# window actually came up.
Param([string]$Version)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path "$PSScriptRoot\..\..").Path

if (-not $Version) {
	$tagged = & git -C $root describe --tags --exact-match --match 'v[0-9]*' 2>$null
	if ($LASTEXITCODE -eq 0 -and $tagged) {
		$tagged = ($tagged | Out-String).Trim()
		$Version = $tagged.Substring(1) # v1.2.3 -> 1.2.3, the Makefile's rule
	} else {
		$last = ((& git -C $root describe --tags --abbrev=0 --match 'v[0-9]*') | Out-String).Trim()
		$Version = $last.Substring(1) + '-dev'
	}
}

# the same files make desktop-assets copies from the Android assets
$assets = Join-Path $root 'app\app\src\main\assets'
$vendor = Join-Path $root 'desktop\frontend\vendor'
New-Item -ItemType Directory -Force $vendor | Out-Null
foreach ($f in 'xterm.js', 'xterm.css', 'addon-fit.js', 'addon-web-links.js', 'addon-unicode11.js', 'JetBrainsMonoNerdFontMono-Regular.woff2', 'LICENSE-xterm.txt', 'LICENSE-nerdfonts.txt') {
	Copy-Item "$assets\$f" $vendor
}

$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$exe = "$root\dist\muxalot-desktop-windows-amd64.exe"
Push-Location "$root\desktop"
& go build -tags production -trimpath -ldflags "-s -w -X main.version=$Version -H windowsgui" -o $exe
$build = $LASTEXITCODE
Pop-Location
if ($build -ne 0) { throw 'go build failed' }

# --check runs the preflight (WebView2 runtime) and prints ok without a window
& $exe --check
if ($LASTEXITCODE -ne 0) { throw 'preflight --check failed' }

# zip with the flat name users get in Explorer; the readme goes next to it
$zip = "$root\dist\muxalot-desktop_${Version}_windows-amd64.zip"
$stage = Join-Path $env:TEMP ("muxalot-zip-" + [guid]::NewGuid().ToString())
New-Item -ItemType Directory $stage | Out-Null
Copy-Item $exe "$stage\muxalot-desktop.exe"
try {
	Compress-Archive -Path "$stage\muxalot-desktop.exe", "$PSScriptRoot\windows-readme.txt" -DestinationPath $zip -Force
} finally {
	Remove-Item $stage -Recurse -Force
}

# smoke: the config dir is %APPDATA%\muxalot, so point APPDATA at a fresh one
$appdata = Join-Path ([IO.Path]::GetTempPath()) ("muxalot-smoke-" + [guid]::NewGuid().ToString())
$env:APPDATA = $appdata
try {
	$p = Start-Process -FilePath $exe -PassThru
	Start-Sleep -Seconds 25
	if ($p.HasExited) {
		throw "the app exited (code $($p.ExitCode)) before the 25 s smoke deadline"
	}
	Stop-Process -Id $p.Id -Force
} finally {
	Remove-Item $appdata -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Output "built and smoked: $zip"
Write-Output "sha256: $((Get-FileHash -Algorithm SHA256 $zip -ErrorAction Stop).Hash.ToLower())"