# Regenerates the runtime logo from the master artwork.
#
#   web/assets/logo.png        master artwork (source, not served)
#   web/public/assets/logo-256.png   generated, used by the UI
#
# Run after replacing the master file:  ./scripts/make-logo.ps1
# The favicon (web/public/logo.ico) is a separate hand-made file and is not touched.

param(
  [string]$Source = (Join-Path $PSScriptRoot "..\web\assets\logo.png"),
  [string]$Target = (Join-Path $PSScriptRoot "..\web\public\assets\logo-256.png"),
  [int]$Size = 256
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Drawing

if (-not (Test-Path $Source)) { throw "Master logo not found: $Source" }

$image = [System.Drawing.Image]::FromFile((Resolve-Path $Source).Path)
try {
  $canvas = New-Object System.Drawing.Bitmap $Size, $Size, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  try {
    $g = [System.Drawing.Graphics]::FromImage($canvas)
    try {
      $g.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
      $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
      $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
      $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
      $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
      $g.DrawImage($image, 0, 0, $Size, $Size)
    } finally { $g.Dispose() }
    $canvas.Save((Join-Path (Split-Path -Parent $Target) (Split-Path -Leaf $Target)), [System.Drawing.Imaging.ImageFormat]::Png)
  } finally { $canvas.Dispose() }
} finally { $image.Dispose() }

$out = Get-Item $Target
"wrote {0} ({1} bytes, {2}x{2})" -f $out.FullName, $out.Length, $Size
