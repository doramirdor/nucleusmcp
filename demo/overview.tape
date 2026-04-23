# overview.tape — hero GIF for README
#
# Regenerate with:
#   vhs demo/overview.tape
#
# Requires:
#   - vhs installed       (https://github.com/charmbracelet/vhs)
#   - nucleusmcp on PATH   (`make install`)
#   - at least two seeded profiles (see demo/seed.sh)

Output demo/overview.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 720
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set PlaybackSpeed 1.0

# Opening card
Type "# NucleusMCP — one connector, many accounts"
Sleep 1.5s
Ctrl+u

# 1. Show built-in + custom connectors
Type "nucleusmcp connectors"
Sleep 400ms
Enter
Sleep 3s

# 2. Show the active profiles (connections)
Type "nucleusmcp list"
Sleep 400ms
Enter
Sleep 3s

# 3. Inspect one — static config + live probe
Type "nucleusmcp info supabase:atlas --no-probe"
Sleep 400ms
Enter
Sleep 4s

# 4. Register with Claude Code (the one-liner)
Type "nucleusmcp install --print   # shows the snippet without writing"
Sleep 400ms
Enter
Sleep 4s

Sleep 2s
