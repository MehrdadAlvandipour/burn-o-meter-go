#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != Darwin ]]; then echo 'The menu bar app requires macOS.' >&2; exit 1; fi
app="dist/Burn.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers"
go build -trimpath -o "$app/Contents/Helpers/burn" ./cmd/burn
swiftc -O -parse-as-library -swift-version 5 -target "$(uname -m)-apple-macosx13.0" macos/Burn.swift -o "$app/Contents/MacOS/Burn" -framework AppKit -framework SwiftUI
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Burn</string>
<key>CFBundleIdentifier</key><string>local.burn.meter</string>
<key>CFBundleName</key><string>Burn</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>0.1.0</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
codesign --force --sign - "$app/Contents/Helpers/burn"
codesign --force --sign - "$app"
printf 'Built %s/dist/Burn.app\nRun: open dist/Burn.app\n' "$PWD"
