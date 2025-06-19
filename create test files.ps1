# Settings
$folderPath = "C:\random_mocap_files"
$fileCount = 1000
$minSizeKB = 1
$maxSizeKB = 20
$extensions = @(".cr", ".c3d", ".txt")

# Create folder if it doesn't exist
if (-not (Test-Path $folderPath)) {
    New-Item -ItemType Directory -Path $folderPath | Out-Null
}

# Function to generate a random filename
function Get-RandomName {
    $chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
    -join ((1..8) | ForEach-Object { $chars[(Get-Random -Minimum 0 -Maximum $chars.Length)] })
}

# Create files
for ($i = 0; $i -lt $fileCount; $i++) {
    $name = Get-RandomName
    $ext = Get-Random -InputObject $extensions
    $sizeKB = Get-Random -Minimum $minSizeKB -Maximum ($maxSizeKB + 1)
    $sizeBytes = $sizeKB * 1024
    $filePath = Join-Path $folderPath "$name$ext"

    # Generate random bytes and write to file
    $bytes = New-Object byte[] $sizeBytes
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    [IO.File]::WriteAllBytes($filePath, $bytes)
}

Write-Host "✅ $fileCount random mocap files created in:`n$folderPath"


# large file
$folderPath = "C:\random_mocap_files"
fsutil file createnew "$folderPath\10GB_mock_file.bin" 10737418240
